package libraryimport

import (
	"encoding/json"
	"errors"
	"regexp"

	"github.com/multica-ai/multica/server/pkg/knowledge"
)

const TaskAdapterVersion = "private-task-v1"

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type SelectedImport struct {
	ObjectID      string `json:"objectId"`
	PreviewDigest string `json:"previewDigest"`
	TransportHash string `json:"transportHash"`
	Entry         Entry  `json:"entry"`
}

type Selection struct {
	SchemaVersion        int              `json:"schemaVersion"`
	SnapshotID           string           `json:"snapshotId"`
	TaskID               string           `json:"taskId"`
	Destination          Destination      `json:"destination"`
	Binding              Binding          `json:"binding"`
	PersonaID            string           `json:"personaId"`
	ProjectContextDigest string           `json:"projectContextDigest"`
	ContextDigest        string           `json:"contextDigest"`
	Provider             string           `json:"provider"`
	AdapterVersion       string           `json:"adapterVersion"`
	Imports              []SelectedImport `json:"imports"`
	Digest               string           `json:"digest"`
}

func (s Selection) Seal() (Selection, error) {
	s.Digest = ""
	digest, err := RecordDigest("shared-library-task-selection-v1", s)
	s.Digest = digest
	return s, err
}

func (s Selection) Validate() error {
	if s.SchemaVersion != 1 || !knowledge.ValidID(s.SnapshotID) || !knowledge.ValidID(s.TaskID) || s.Binding.TaskID != s.TaskID || !knowledge.ValidID(s.Binding.RuntimeID) || !knowledge.ValidID(s.PersonaID) || !knowledge.ValidID(s.Provider) || s.AdapterVersion != TaskAdapterVersion || !digestPattern.MatchString(s.ContextDigest) || !digestPattern.MatchString(s.ProjectContextDigest) || len(s.Imports) > knowledge.MaxBindings {
		return errors.New("invalid_private_selection")
	}
	if err := s.Destination.Validate(); err != nil {
		return err
	}
	if s.Binding.ProjectID != "" && !knowledge.ValidID(s.Binding.ProjectID) {
		return errors.New("invalid_private_project")
	}
	if ref := s.Binding.PersonaRef; ref != nil && (ref.PersonaID != s.PersonaID || ref.ServerID != s.Destination.ServerID || ref.WorkspaceID != s.Destination.WorkspaceID) {
		return errors.New("invalid_private_persona")
	}
	seen := map[string]bool{}
	files, total := 0, int64(0)
	for _, item := range s.Imports {
		if !knowledge.ValidID(item.ObjectID) || seen[item.ObjectID] || len(item.Entry.Files) == 0 || len(item.Entry.Files) > knowledge.MaxFiles || !digestPattern.MatchString(item.PreviewDigest) || !digestPattern.MatchString(item.TransportHash) || !digestPattern.MatchString(item.Entry.ContentHash) || s.Binding.ProjectID == "" {
			return errors.New("invalid_private_import")
		}
		seen[item.ObjectID] = true
		files += len(item.Entry.Files)
		for _, file := range item.Entry.Files {
			if file.SizeBytes < 0 || file.SizeBytes > knowledge.MaxFileBytes {
				return errors.New("runtime_limit")
			}
			total += file.SizeBytes
		}
	}
	if files > 1024 || total > knowledge.MaxBytes {
		return errors.New("runtime_limit")
	}
	sealed, err := s.Seal()
	if err != nil || sealed.Digest != s.Digest {
		return errors.New("private_selection_digest_mismatch")
	}
	return nil
}

type Object struct {
	Receipt   Receipt `json:"receipt"`
	Selection Request `json:"selection"`
	Bundle    Bundle  `json:"bundle"`
}

func (o Object) Verify(s Selection, item SelectedImport) error {
	request := o.Selection
	digest, err := request.Preview.Digest()
	if err != nil || digest != item.PreviewDigest || request.Consent.PreviewDigest != digest || request.Consent.ActorID != s.Destination.OwnerID || request.Preview.Validate() != nil || request.Verify() != nil {
		return errors.New("private_import_integrity_failure")
	}
	expectedEntry, _ := json.Marshal(item.Entry)
	actualEntry, _ := json.Marshal(request.Preview.Entry)
	if string(expectedEntry) != string(actualEntry) || request.Preview.Destination != s.Destination || request.Preview.Binding.ProjectID != s.Binding.ProjectID || request.Preview.Binding.TaskID != s.TaskID || request.Preview.Binding.RuntimeID != s.Binding.RuntimeID {
		return errors.New("private_import_identity_mismatch")
	}
	if p := request.Preview.Binding.PersonaRef; p != nil && p.PersonaID != s.PersonaID {
		return errors.New("private_import_persona_mismatch")
	}
	expected := (Payload{Entry: item.Entry, Content: request.Content}).Bundle(item.ObjectID)
	a, _ := json.Marshal(expected)
	b, _ := json.Marshal(o.Bundle)
	if string(a) != string(b) || expected.Hash != item.TransportHash || o.Receipt.ObjectID != item.ObjectID || o.Receipt.AuthorityID != s.Destination.ServerID || o.Receipt.ApprovalRef != "private-import:"+item.ObjectID || o.Receipt.PreviewDigest != item.PreviewDigest || o.Receipt.ContentHash != item.Entry.ContentHash || o.Receipt.TransportHash != item.TransportHash || o.Receipt.Status != "stored" {
		return errors.New("private_import_integrity_failure")
	}
	return nil
}
