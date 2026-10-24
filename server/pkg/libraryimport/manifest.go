package libraryimport

import (
	"errors"
	"strings"

	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/multica-ai/multica/server/pkg/skillbundle"
)

const SourcePrivate = "private"
const MaxRequestBytes = 16 << 20

type Provenance struct {
	OriginAuthorityID string `json:"originAuthorityId"`
	OriginSourceID    string `json:"originSourceId"`
	OriginEntryID     string `json:"originEntryId"`
	OriginKind        string `json:"originKind"`
	ResolverID        string `json:"resolverId"`
	SourceRevision    int64  `json:"sourceRevision"`
	AdapterVersion    string `json:"adapterVersion"`
	Verification      string `json:"verification"`
}

type File struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
	Mode      string `json:"mode"`
}

type Entry struct {
	SchemaVersion int        `json:"schemaVersion"`
	EntryID       string     `json:"entryId"`
	SourceID      string     `json:"sourceId"`
	Kind          string     `json:"kind"`
	DisplayName   string     `json:"displayName"`
	Format        string     `json:"format"`
	EntryPoint    string     `json:"entryPoint"`
	Files         []File     `json:"files"`
	Requirements  []string   `json:"requirements"`
	ContentHash   string     `json:"contentHash"`
	Provenance    Provenance `json:"provenance"`
	Status        string     `json:"status"`
	ReasonCode    string     `json:"reasonCode"`
}

type Payload struct {
	Entry   Entry             `json:"entry"`
	Content map[string]string `json:"content"`
}

func (p Payload) Verify() error {
	e := p.Entry
	if e.SchemaVersion != 1 || !knowledge.ValidID(e.EntryID) || !knowledge.ValidID(e.SourceID) || e.DisplayName == "" || len(e.DisplayName) > 1024 {
		return errors.New("invalid_manifest")
	}
	origin := e.Provenance
	if !knowledge.ValidID(origin.OriginAuthorityID) || origin.OriginSourceID != e.SourceID || origin.OriginEntryID != e.EntryID || !knowledge.ValidID(origin.ResolverID) || origin.SourceRevision < 1 || origin.AdapterVersion == "" || (origin.Verification != "observed" && origin.Verification != "verified") {
		return errors.New("invalid_provenance")
	}
	switch origin.OriginKind {
	case "machine_local", "repo_relative", "imported_bundle":
	case "managed_bundle":
		return errors.New("policy_unavailable")
	default:
		return errors.New("invalid_provenance")
	}
	if len(e.Requirements) != 0 || !((e.Kind == "rule" && e.Format == "plain-rule-v1") || (e.Kind == "skill" && e.Format == "native-skill-text-v1" && e.EntryPoint == "SKILL.md")) {
		return errors.New("unsupported_format")
	}
	if len(p.Content) != len(e.Files) {
		return errors.New("digest_mismatch")
	}
	files := make([]knowledge.TextFile, 0, len(e.Files))
	for _, f := range e.Files {
		data, ok := p.Content[f.Path]
		if !ok || f.SizeBytes != int64(len(data)) {
			return errors.New("digest_mismatch")
		}
		files = append(files, knowledge.TextFile{Path: f.Path, Mode: f.Mode, SizeBytes: len(data), SHA256: f.SHA256, Content: data})
	}
	hash, err := knowledge.ContentHash(e.Kind, e.Format, e.EntryPoint, files)
	if err != nil {
		return err
	}
	if hash != e.ContentHash {
		return errors.New("digest_mismatch")
	}
	return nil
}

func (p Payload) Transport(objectID string) (skillbundle.Skill, skillbundle.Manifest) {
	skill := skillbundle.Skill{ID: objectID, Source: SourcePrivate, Name: p.Entry.DisplayName, Content: p.Content[p.Entry.EntryPoint]}
	for _, f := range p.Entry.Files {
		if f.Path != p.Entry.EntryPoint {
			skill.Files = append(skill.Files, skillbundle.File{Path: f.Path, Content: p.Content[f.Path]})
		}
	}
	return skill, skillbundle.BuildManifest(skill)
}

func ValidHash(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	for _, c := range s[7:] {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

type Bundle struct {
	ID        string       `json:"id"`
	Source    string       `json:"source"`
	Name      string       `json:"name"`
	Hash      string       `json:"hash"`
	SizeBytes int64        `json:"size_bytes"`
	Content   string       `json:"content"`
	Files     []BundleFile `json:"files"`
}

type BundleFile struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

func (p Payload) Bundle(objectID string) Bundle {
	skill, manifest := p.Transport(objectID)
	bundle := Bundle{ID: skill.ID, Source: skill.Source, Name: skill.Name, Hash: manifest.Hash, SizeBytes: manifest.SizeBytes, Content: skill.Content, Files: make([]BundleFile, 0, len(skill.Files))}
	for _, f := range skill.Files {
		bundle.Files = append(bundle.Files, BundleFile{Path: f.Path, Content: f.Content, SHA256: knowledge.Sum([]byte(f.Content)), SizeBytes: int64(len(f.Content))})
	}
	return bundle
}
