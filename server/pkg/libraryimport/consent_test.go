package libraryimport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/knowledge"
)

func testRequest(now time.Time) Request {
	text := "Review selected changes.\n"
	entry := Entry{SchemaVersion: 1, EntryID: "ent_review", SourceID: "src_personal", Kind: "rule", DisplayName: "Review guidance", Format: "plain-rule-v1", EntryPoint: "rules/review.md", Files: []File{{Path: "rules/review.md", SHA256: knowledge.Sum([]byte(text)), SizeBytes: int64(len(text)), Mode: "text"}}, Requirements: []string{}, ContentHash: "sha256:26eb5fd1696af1e0c83b85a2933f699fb93c286ba0ca4e0056f528e174cbb94d", Provenance: Provenance{OriginAuthorityID: "ao_local", OriginSourceID: "src_personal", OriginEntryID: "ent_review", OriginKind: "machine_local", ResolverID: "resolver1", SourceRevision: 1, AdapterVersion: "1", Verification: "observed"}}
	preview := Preview{SchemaVersion: 1, AttemptID: "attempt1", Operation: "private_import", SourceResolverID: "resolver1", Destination: Destination{ServerID: "server1", Origin: "https://example.test", WorkspaceID: "workspace1", OwnerID: "owner1", Audience: "owner_and_explicit_task"}, Binding: Binding{ProjectID: "project1", TaskID: "task1", RuntimeID: "runtime1"}, Entry: entry, TotalBytes: int64(len(text)), Excluded: []string{}}
	digest, _ := preview.Digest()
	consent, err := Confirm(preview, digest, "owner1", now)
	if err != nil {
		panic(err)
	}
	return Request{Preview: preview, Consent: consent, Content: map[string]string{entry.EntryPoint: text}}
}

func TestFrozenEntryAndTransportHashes(t *testing.T) {
	for _, tc := range []struct{ text, hash string }{
		{"Review selected changes.\n", "sha256:26eb5fd1696af1e0c83b85a2933f699fb93c286ba0ca4e0056f528e174cbb94d"},
		{"Review selected changes carefully.\n", "sha256:d7108c9dd9c4b5ec1c67a81800dd1a9fb9f1e0fdf27499fff7f80873d18016e9"},
	} {
		r := testRequest(time.Now())
		r.Content[r.Preview.Entry.EntryPoint] = tc.text
		r.Preview.TotalBytes = int64(len(tc.text))
		r.Preview.Entry.ContentHash = tc.hash
		r.Preview.Entry.Files[0].SHA256 = knowledge.Sum([]byte(tc.text))
		r.Preview.Entry.Files[0].SizeBytes = int64(len(tc.text))
		if err := r.Verify(); err != nil {
			t.Fatal(err)
		}
		p := Payload{Entry: r.Preview.Entry, Content: r.Content}
		_, before := p.Transport("private1")
		if before.Hash == tc.hash {
			t.Fatal("transport mislabeled")
		}
		p.Entry.DisplayName = "Renamed"
		p.Entry.EntryID = "entry_other"
		p.Entry.SourceID = "source_other"
		p.Entry.Provenance.OriginEntryID = p.Entry.EntryID
		p.Entry.Provenance.OriginSourceID = p.Entry.SourceID
		if err := p.Verify(); err != nil {
			t.Fatal("presentation changed portable hash", err)
		}
		_, after := p.Transport("private1")
		if before.Hash == after.Hash {
			t.Fatal("transport ignored metadata")
		}
	}
}

func TestConsentPinsEveryDisclosedField(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	for name, change := range map[string]func(*Request){
		"server":    func(r *Request) { r.Preview.Destination.ServerID = "other" },
		"origin":    func(r *Request) { r.Preview.Destination.Origin = "https://other.test" },
		"workspace": func(r *Request) { r.Preview.Destination.WorkspaceID = "other" },
		"owner":     func(r *Request) { r.Preview.Destination.OwnerID = "other" },
		"audience":  func(r *Request) { r.Preview.Destination.Audience = "workspace" },
		"actor":     func(r *Request) { r.Consent.ActorID = "admin" },
		"file":      func(r *Request) { r.Preview.Entry.Files[0].Path = "other.md" },
		"hash":      func(r *Request) { r.Preview.Entry.ContentHash = knowledge.Sum([]byte("other")) },
		"project":   func(r *Request) { r.Preview.Binding.ProjectID = "other" },
		"persona": func(r *Request) {
			r.Preview.Binding.PersonaRef = &PersonaRef{ServerID: "server1", WorkspaceID: "workspace1", PersonaID: "persona1"}
		},
		"task":            func(r *Request) { r.Preview.Binding.TaskID = "other" },
		"runtime":         func(r *Request) { r.Preview.Binding.RuntimeID = "other" },
		"attempt":         func(r *Request) { r.Preview.AttemptID = "other" },
		"resolver":        func(r *Request) { r.Preview.SourceResolverID = "other" },
		"excluded":        func(r *Request) { r.Preview.Excluded = []string{"unsupported1"} },
		"name":            func(r *Request) { r.Preview.Entry.DisplayName = "Other" },
		"extended expiry": func(r *Request) { r.Consent.ExpiresAt = now.Add(time.Hour).Format(timestampLayout) },
	} {
		t.Run(name, func(t *testing.T) {
			r := testRequest(now)
			change(&r)
			if r.VerifyConsent(now) == nil {
				t.Fatal("changed disclosure accepted")
			}
		})
	}
	r := testRequest(now)
	if err := r.VerifyConsent(now); err != nil {
		t.Fatal(err)
	}
	if r.VerifyConsent(now.Add(ConsentLifetime)) == nil || r.VerifyConsent(now.Add(-time.Second)) == nil {
		t.Fatal("invalid receipt time")
	}
	digest, _ := r.Preview.Digest()
	if _, err := Confirm(r.Preview, digest, "admin", now); err == nil {
		t.Fatal("admin confirmed")
	}
	if _, err := Confirm(r.Preview, "stale", "owner1", now); err == nil {
		t.Fatal("undisplayed consent")
	}
}

func TestRejectUnsafePayloadAndAmbiguousJSON(t *testing.T) {
	for name, change := range map[string]func(*Request){
		"executable":         func(r *Request) { r.Preview.Entry.Files[0].Mode = "executable" },
		"requirements":       func(r *Request) { r.Preview.Entry.Requirements = []string{"shell"} },
		"hook":               func(r *Request) { r.Preview.Entry.Kind = "hook" },
		"persona conversion": func(r *Request) { r.Preview.Entry.Kind = "persona" },
		"schema":             func(r *Request) { r.Preview.Entry.SchemaVersion = 2 },
		"extra content":      func(r *Request) { r.Content["secret.md"] = "secret" },
		"path":               func(r *Request) { r.Preview.Entry.Files[0].Path = "../escape" },
		"total":              func(r *Request) { r.Preview.TotalBytes++ },
		"changed content":    func(r *Request) { r.Content[r.Preview.Entry.EntryPoint] = "different" },
		"alias": func(r *Request) {
			r.Preview.Entry.Files = append(r.Preview.Entry.Files, File{Path: "RULES/Review.md", SHA256: knowledge.Sum([]byte("x")), SizeBytes: 1, Mode: "text"})
			r.Content["RULES/Review.md"] = "x"
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := testRequest(time.Now())
			change(&r)
			if r.Verify() == nil {
				t.Fatal("unsafe payload accepted")
			}
		})
	}
	r := testRequest(time.Now())
	raw, _ := json.Marshal(r)
	if _, err := DecodeRequest(raw); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{string(raw) + " {}", `{"preview":{},"preview":{}}`, strings.Replace(string(raw), `"schemaVersion":1`, `"schemaVersion":1,"behavior":"run"`, 1)} {
		if _, err := DecodeRequest([]byte(raw)); err == nil {
			t.Fatal("ambiguous JSON accepted")
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func response(status int, v any) *http.Response {
	raw, _ := json.Marshal(v)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw))}
}

func TestUploadFailsBeforeDisclosure(t *testing.T) {
	for _, mode := range []string{"no consent", "unsupported", "different destination", "redirect", "offline", "cancelled", "ok", "bad receipt", "bad transport", "bad object id", "bad approval"} {
		t.Run(mode, func(t *testing.T) {
			input := testRequest(time.Now())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var methods []string
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				methods = append(methods, r.Method)
				if r.URL.Host != "example.test" || r.Header.Get("X-Workspace-ID") != "workspace1" {
					t.Fatal("wrong destination")
				}
				if r.Method == http.MethodGet {
					if r.ContentLength != 0 {
						t.Fatal("inventory in preflight")
					}
					cap := Capability{Supported: true, Destination: input.Preview.Destination}
					switch mode {
					case "unsupported":
						cap.Supported = false
					case "different destination":
						cap.Destination.ServerID = "other"
					case "redirect":
						res := response(302, nil)
						res.Header.Set("Location", "https://other.test")
						return res, nil
					case "offline":
						return nil, io.ErrUnexpectedEOF
					case "cancelled":
						cancel()
					}
					return response(200, cap), nil
				}
				raw, _ := io.ReadAll(r.Body)
				decoded, err := DecodeRequest(raw)
				if err != nil || decoded.Verify() != nil {
					t.Fatal("upload mutated bytes")
				}
				_, transport := (Payload{Entry: input.Preview.Entry, Content: input.Content}).Transport("object1")
				receipt := Receipt{ObjectID: "object1", AuthorityID: "server1", ApprovalRef: "private-import:object1", PreviewDigest: input.Consent.PreviewDigest, ContentHash: input.Preview.Entry.ContentHash, TransportHash: transport.Hash, Status: "stored", ReasonCode: "unsupported_provider"}
				if mode == "bad receipt" {
					receipt.ContentHash = "wrong"
				}
				if mode == "bad transport" {
					receipt.TransportHash = knowledge.Sum([]byte("wrong transport"))
				}
				if mode == "bad object id" {
					receipt.ObjectID = "../other"
					_, invalid := (Payload{Entry: input.Preview.Entry, Content: input.Content}).Transport(receipt.ObjectID)
					receipt.TransportHash = invalid.Hash
					receipt.ApprovalRef = "private-import:" + receipt.ObjectID
				}
				if mode == "bad approval" {
					receipt.ApprovalRef = "unrelated"
				}
				return response(201, receipt), nil
			})}
			if mode == "no consent" {
				input.Consent = Consent{}
			}
			_, err := Upload(ctx, client, make(http.Header), input)
			if (err == nil) != (mode == "ok") {
				t.Fatalf("err=%v", err)
			}
			want := 1
			if mode == "no consent" {
				want = 0
			}
			if mode == "ok" || mode == "bad receipt" || mode == "bad transport" || mode == "bad object id" || mode == "bad approval" {
				want = 2
			}
			if len(methods) != want {
				t.Fatalf("requests=%v want=%d", methods, want)
			}
		})
	}
}

func TestUploadFreezesSelectionAndHonorsExpiryDeadline(t *testing.T) {
	input := testRequest(time.Now())
	original := input.Content["rules/review.md"]
	_, transport := (Payload{Entry: input.Preview.Entry, Content: input.Content}).Transport("object1")
	receipt := Receipt{ObjectID: "object1", AuthorityID: "server1", ApprovalRef: "private-import:object1", PreviewDigest: input.Consent.PreviewDigest, ContentHash: input.Preview.Entry.ContentHash, TransportHash: transport.Hash, Status: "stored", ReasonCode: "unsupported_provider"}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		expires, _ := time.Parse(timestampLayout, input.Consent.ExpiresAt)
		deadline, ok := req.Context().Deadline()
		if !ok || deadline != expires {
			t.Fatal("upload can continue beyond consent expiry")
		}
		if req.Method == http.MethodGet {
			input.Content["rules/review.md"] = "changed during preflight"
			return response(200, Capability{Supported: true, Destination: input.Preview.Destination}), nil
		}
		raw, _ := io.ReadAll(req.Body)
		sent, err := DecodeRequest(raw)
		if err != nil || sent.Content["rules/review.md"] != original || sent.Verify() != nil {
			t.Fatal("upload did not freeze inspected bytes")
		}
		return response(201, receipt), nil
	})}
	if _, err := Upload(context.Background(), client, nil, input); err != nil {
		t.Fatal(err)
	}
}

func TestDestinationMustBeAnExactAuthenticatedOrigin(t *testing.T) {
	input := testRequest(time.Now())
	for _, origin := range []string{"http://remote.test", "https://example.test/", "https://example.test#", "https://example.test?", "https://user:secret@example.test", "https://example.test/path", "https://example.test?redirect=1"} {
		destination := input.Preview.Destination
		destination.Origin = origin
		if destination.Validate() == nil {
			t.Fatalf("accepted %q", origin)
		}
	}
}
