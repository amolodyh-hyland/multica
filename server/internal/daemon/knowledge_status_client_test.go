package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/knowledge"
)

func TestKnowledgeStatusTransportIsMetadataOnly(t *testing.T) {
	client := NewClient("https://daemon.test")
	client.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "\"content\"") || strings.Contains(string(raw), "/private") {
			t.Error("source bytes or path crossed transport")
		}
		var wire map[string]json.RawMessage
		if json.Unmarshal(raw, &wire) != nil || len(wire) != 1 || wire["results"] == nil {
			t.Error("unexpected metadata envelope")
		}
		if !strings.Contains(r.Header.Get("X-Client-Capabilities"), "knowledge-status-v1") {
			t.Error("missing status capability")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	err := client.ReportKnowledgeStatus(context.Background(), "runtime1", "request1", []knowledge.SourceStatus{{ResourceID: "resource1", ReferenceHash: "sha256:" + strings.Repeat("a", 64), Status: "stale", ContentHash: "sha256:" + strings.Repeat("b", 64), Reason: "changed"}})
	if err != nil {
		t.Fatal(err)
	}
}
