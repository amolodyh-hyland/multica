package libraryimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"time"

	"github.com/multica-ai/multica/server/pkg/knowledge"
)

const ConsentLifetime = 15 * time.Minute
const timestampLayout = "2006-01-02T15:04:05.000Z"

type Destination struct {
	ServerID    string `json:"serverId"`
	Origin      string `json:"origin"`
	WorkspaceID string `json:"workspaceId"`
	OwnerID     string `json:"ownerId"`
	Audience    string `json:"audience"`
}

type PersonaRef struct {
	ServerID    string `json:"serverId"`
	WorkspaceID string `json:"workspaceId"`
	PersonaID   string `json:"personaId"`
}

type Binding struct {
	ProjectID  string      `json:"projectId"`
	PersonaRef *PersonaRef `json:"personaRef,omitempty"`
	TaskID     string      `json:"taskId"`
	RuntimeID  string      `json:"runtimeId"`
}

type Preview struct {
	SchemaVersion    int         `json:"schemaVersion"`
	AttemptID        string      `json:"attemptId"`
	Operation        string      `json:"operation"`
	SourceResolverID string      `json:"sourceResolverId"`
	Destination      Destination `json:"destination"`
	Binding          Binding     `json:"binding"`
	Entry            Entry       `json:"entry"`
	TotalBytes       int64       `json:"totalBytes"`
	Excluded         []string    `json:"excluded"`
}

type Consent struct {
	ActorID       string `json:"actorId"`
	PreviewDigest string `json:"previewDigest"`
	ApprovedAt    string `json:"approvedAt"`
	ExpiresAt     string `json:"expiresAt"`
}

type Request struct {
	Preview Preview           `json:"preview"`
	Consent Consent           `json:"consent"`
	Content map[string]string `json:"content"`
}

type Capability struct {
	TaskActivation       string      `json:"taskActivation"`
	TaskActivationReason string      `json:"taskActivationReason"`
	Reload               string      `json:"reload"`
	Supported            bool        `json:"supported"`
	ReasonCode           string      `json:"reasonCode,omitempty"`
	Destination          Destination `json:"destination"`
}

type Receipt struct {
	ObjectID      string `json:"objectId"`
	AuthorityID   string `json:"authorityId"`
	ApprovalRef   string `json:"approvalRef"`
	PreviewDigest string `json:"previewDigest"`
	ContentHash   string `json:"contentHash"`
	TransportHash string `json:"transportHash"`
	Status        string `json:"status"`
	ReasonCode    string `json:"reasonCode"`
}

func (d Destination) Validate() error {
	u, err := url.Parse(d.Origin)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" || d.Origin != u.Scheme+"://"+u.Host || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) || !knowledge.ValidID(d.ServerID) || !knowledge.ValidID(d.WorkspaceID) || !knowledge.ValidID(d.OwnerID) || d.Audience != "owner_and_explicit_task" {
		return errors.New("invalid_destination")
	}
	return nil
}

func (p Preview) Digest() (string, error) { return RecordDigest("shared-library-transfer-v1", p) }

func (p Preview) Validate() error {
	if p.SchemaVersion != 1 || !knowledge.ValidID(p.AttemptID) || p.Operation != "private_import" || p.SourceResolverID != p.Entry.Provenance.ResolverID || !knowledge.ValidID(p.SourceResolverID) || !knowledge.ValidID(p.Binding.ProjectID) || !knowledge.ValidID(p.Binding.TaskID) || !knowledge.ValidID(p.Binding.RuntimeID) || p.TotalBytes < 0 || p.TotalBytes > knowledge.MaxBytes || len(p.Excluded) > 256 {
		return errors.New("invalid_preview")
	}
	if err := p.Destination.Validate(); err != nil {
		return err
	}
	if ref := p.Binding.PersonaRef; ref != nil && (ref.ServerID != p.Destination.ServerID || ref.WorkspaceID != p.Destination.WorkspaceID || !knowledge.ValidID(ref.PersonaID)) {
		return errors.New("invalid_persona_binding")
	}
	return nil
}

func Confirm(p Preview, displayedDigest, actorID string, now time.Time) (Consent, error) {
	if err := p.Validate(); err != nil {
		return Consent{}, err
	}
	digest, err := p.Digest()
	if err != nil {
		return Consent{}, err
	}
	if displayedDigest != digest || actorID != p.Destination.OwnerID {
		return Consent{}, errors.New("consent_mismatch")
	}
	now = now.UTC().Truncate(time.Millisecond)
	return Consent{ActorID: actorID, PreviewDigest: digest, ApprovedAt: now.Format(timestampLayout), ExpiresAt: now.Add(ConsentLifetime).Format(timestampLayout)}, nil
}

func (r Request) VerifyConsent(now time.Time) error {
	if err := r.Preview.Validate(); err != nil {
		return err
	}
	digest, err := r.Preview.Digest()
	if err != nil {
		return err
	}
	c := r.Consent
	approved, e1 := time.Parse(timestampLayout, c.ApprovedAt)
	expires, e2 := time.Parse(timestampLayout, c.ExpiresAt)
	if e1 != nil || e2 != nil || approved.Format(timestampLayout) != c.ApprovedAt || expires.Format(timestampLayout) != c.ExpiresAt || approved.After(now) || !now.Before(expires) || expires.Sub(approved) != ConsentLifetime || c.ActorID != r.Preview.Destination.OwnerID || c.PreviewDigest != digest {
		return errors.New("consent_mismatch_or_expired")
	}
	return nil
}

func (r Request) Verify() error {
	if err := (Payload{Entry: r.Preview.Entry, Content: r.Content}).Verify(); err != nil {
		return err
	}
	var total int64
	for _, f := range r.Preview.Entry.Files {
		total += f.SizeBytes
	}
	if total != r.Preview.TotalBytes {
		return errors.New("file_set_mismatch")
	}
	return nil
}

func DecodeRequest(raw []byte) (Request, error) {
	var r Request
	if len(raw) > MaxRequestBytes {
		return r, errors.New("payload_limit")
	}
	if _, err := CanonicalJSON(raw); err != nil {
		return r, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return r, errors.New("trailing_json")
	}
	return r, nil
}
