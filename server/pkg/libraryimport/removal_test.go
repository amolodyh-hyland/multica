package libraryimport

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRemovalRequiresSeparateExactConsent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	imported := testRequest(now)
	preview := RemovalPreview{Operation: "private_import_remove", ObjectID: "object1", Import: imported.Preview}
	digest, err := preview.Digest()
	if err != nil {
		t.Fatal(err)
	}
	consent, err := ConfirmRemoval(preview, digest, preview.Import.Destination.OwnerID, now)
	if err != nil {
		t.Fatal(err)
	}
	request := RemovalRequest{Preview: preview, Consent: consent}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRemovalRequest(raw)
	if err != nil || decoded.VerifyConsent(now) != nil {
		t.Fatalf("valid removal: %v", err)
	}
	for _, scenario := range []string{"import consent", "object", "destination", "file", "actor", "operation", "expired", "future"} {
		t.Run(scenario, func(t *testing.T) {
			r, _ := DecodeRemovalRequest(raw)
			at := now
			switch scenario {
			case "import consent":
				r.Consent = imported.Consent
			case "object":
				r.Preview.ObjectID = "other"
			case "destination":
				r.Preview.Import.Destination.Origin = "https://other.test"
			case "file":
				r.Preview.Import.Entry.Files[0].Path = "other.md"
			case "actor":
				r.Consent.ActorID = "other"
			case "operation":
				r.Preview.Operation = "private_import"
			case "expired":
				at = now.Add(ConsentLifetime)
			case "future":
				at = now.Add(-time.Millisecond)
			}
			if r.VerifyConsent(at) == nil {
				t.Fatal("changed removal authorized")
			}
		})
	}
	if _, err := ConfirmRemoval(preview, "undisplayed", preview.Import.Destination.OwnerID, now); err == nil {
		t.Fatal("undisplayed digest confirmed")
	}
	if _, err := ConfirmRemoval(preview, digest, "other", now); err == nil {
		t.Fatal("other owner confirmed")
	}
}
