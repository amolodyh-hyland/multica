package libraryimport

import (
	"encoding/json"
	"errors"
)

type TaskSnapshot struct {
	Selection Selection `json:"selection"`
	Payloads  []Payload `json:"payloads"`
}

func (s TaskSnapshot) Validate() error {
	if err := s.Selection.Validate(); err != nil {
		return err
	}
	if len(s.Payloads) != len(s.Selection.Imports) {
		return errors.New("private_snapshot_incomplete")
	}
	for i, payload := range s.Payloads {
		selected := s.Selection.Imports[i]
		if err := payload.Verify(); err != nil {
			return err
		}
		actual, _ := json.Marshal(payload.Entry)
		expected, _ := json.Marshal(selected.Entry)
		_, transport := payload.Transport(selected.ObjectID)
		if string(actual) != string(expected) || transport.Hash != selected.TransportHash {
			return errors.New("private_snapshot_changed")
		}
	}
	return nil
}

// No native-provider evidence is bundled yet. A version string alone cannot
// establish which native source wins or which bytes the provider consumes.
func RequireTaskProvider(provider, version string) error {
	if provider != "claude" {
		return errors.New("unsupported_provider")
	}
	if version == "" {
		return errors.New("unknown_provider_version")
	}
	return errors.New("native_conformance_missing")
}

type RuntimeBinding struct {
	ObjectID      string `json:"objectId"`
	EntryID       string `json:"entryId"`
	SnapshotID    string `json:"snapshotId"`
	ContentHash   string `json:"contentHash"`
	TransportHash string `json:"transportHash"`
	Generation    string `json:"generation"`
	NativeName    string `json:"nativeName,omitempty"`
	Path          string `json:"path,omitempty"`
	Invocation    string `json:"invocation"`
	Status        string `json:"status"`
	ReasonCode    string `json:"reasonCode"`
}

func (s TaskSnapshot) VerifyBindings(bindings []RuntimeBinding) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if len(bindings) != len(s.Selection.Imports) {
		return errors.New("private_library_binding_incomplete")
	}
	seen := map[string]bool{}
	for _, binding := range bindings {
		found := false
		for _, selected := range s.Selection.Imports {
			if binding.ObjectID != selected.ObjectID {
				continue
			}
			found = true
			if seen[binding.ObjectID] || binding.EntryID != selected.Entry.EntryID || binding.ContentHash != selected.Entry.ContentHash || binding.TransportHash != selected.TransportHash || binding.SnapshotID != s.Selection.SnapshotID || binding.Generation == "" {
				return errors.New("private_library_binding_identity_mismatch")
			}
			seen[binding.ObjectID] = true
		}
		if !found {
			return errors.New("private_library_binding_identity_mismatch")
		}
		if binding.Status != "ready" {
			return errors.New("private_library_controller_not_ready")
		}
	}
	return nil
}
