package libraryimport

import (
	"strings"
	"testing"
	"time"
)

func TestPrivateImportRejectsOversizedMetadataWithValidContent(t *testing.T) {
	for _, field := range []string{"status", "reason", "adapter", "path"} {
		t.Run(field, func(t *testing.T) {
			input := testRequest(time.Now())
			switch field {
			case "status":
				input.Preview.Entry.Status = strings.Repeat("x", 12<<20)
			case "reason":
				input.Preview.Entry.ReasonCode = strings.Repeat("x", MaxMetadataTokenBytes+1)
			case "adapter":
				input.Preview.Entry.Provenance.AdapterVersion = strings.Repeat("x", MaxMetadataTokenBytes+1)
			case "path":
				input.Preview.Entry.Files[0].Path = strings.Repeat("x", MaxMetadataTextBytes+1)
			}
			if err := input.Verify(); err == nil || err.Error() != "metadata_limit" {
				t.Fatalf("unbounded %s: %v", field, err)
			}
		})
	}
}

func TestPrivateSelectionBoundsMetadataBeforeSealing(t *testing.T) {
	input := testRequest(time.Now())
	selection := Selection{Destination: input.Preview.Destination}
	entry := input.Preview.Entry
	entry.Status = strings.Repeat("x", 12<<20)
	selection.Imports = []SelectedImport{{ObjectID: "object", PreviewDigest: input.Consent.PreviewDigest, TransportHash: input.Consent.PreviewDigest, Entry: entry}}
	if _, err := selection.Seal(); err == nil || err.Error() != "metadata_limit" {
		t.Fatalf("oversized metadata was hashed: %v", err)
	}
	entry.Status = ""
	entry.DisplayName = strings.Repeat("<", MaxMetadataTextBytes)
	entry.Files = nil
	for i := 0; i < 32; i++ {
		f := input.Preview.Entry.Files[0]
		f.Path = strings.Repeat("p", MaxMetadataTextBytes)
		entry.Files = append(entry.Files, f)
	}
	if err := entry.ValidateMetadata(); err != nil {
		t.Fatal(err)
	}
	selection.Imports = nil
	for range 32 {
		selection.Imports = append(selection.Imports, SelectedImport{ObjectID: "object", PreviewDigest: input.Consent.PreviewDigest, TransportHash: input.Consent.PreviewDigest, Entry: entry})
	}
	if _, err := selection.Seal(); err == nil || err.Error() != "runtime_limit" {
		t.Fatalf("aggregate metadata was hashed: %v", err)
	}
}
