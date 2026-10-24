package libraryimport

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestLibraryGoldenRecordVectors(t *testing.T) {
	raw := `{"entries":[{"contentHash":"sha256:` + strings.Repeat("0", 64) + `","entryId":"ent_1"}],"purposes":["load_context"],"rootIdentity":{"resolverId":"resolver_1","rootGeneration":1,"rootRef":"root_1"},"policyRevision":""}`
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	for domain, want := range map[string]string{"shared-library-approval-v1": "sha256:686699d0a9718c80bdac9d9285292f3462beda953f22cb127e539d65642f008e", "shared-library-transfer-v1": "sha256:8cc886c118ccaea745e707f0837b3d6b29e5942c1baee9006d61904a52cc562b", "shared-library-session-v1": "sha256:a820c526f78055ecbf64911acde760c1e61e0b4ff1e118552f0edcf15777aa82"} {
		got, err := RecordDigest(domain, value)
		if err != nil || got != want {
			t.Fatalf("%s=%s err=%v", domain, got, err)
		}
	}
}
func TestLibraryCanonicalJSON(t *testing.T) {
	got, err := CanonicalJSON([]byte(`{"z":"é/\n\t\u0000","a":{"b":2,"a":1}}`))
	if err != nil || string(got) != `{"a":{"a":1,"b":2},"z":"é/\u000a\u0009\u0000"}` {
		t.Fatalf("canonical=%s err=%v", got, err)
	}
	for _, raw := range []string{`{"a":1,"a":2}`, `{"v":1.0}`, `{"v":1e3}`, `{"v":"\ud800"}`, `{"v":"\udc00"}`, `{} {}`, `{"v":9223372036854775808}`} {
		if _, err := CanonicalJSON([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := CanonicalJSON([]byte(`{"v":"\ud83d\ude00"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestFrozenManifestFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/entry-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var entry Entry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	payload := Payload{Entry: entry, Content: map[string]string{"rules/review.md": "Review selected changes.\n"}}
	if err := payload.Verify(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := CanonicalJSON(raw)
	actual, _ := CanonicalJSON(encoded)
	if string(actual) != string(expected) {
		t.Fatal("portable manifest field drift")
	}
}
