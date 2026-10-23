package knowledge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	File         = "knowledge_file"
	Folder       = "knowledge_folder"
	Skill        = "knowledge_skill"
	MaxBindings  = 64
	MaxFileBytes = 1 << 20
	MaxBytes     = 8 << 20
	MaxFiles     = 256
	MaxNodes     = 4096
	MaxDepth     = 4
)

var opaqueID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var hashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func IsType(kind string) bool { return kind == File || kind == Folder || kind == Skill }
func ValidID(id string) bool  { return opaqueID.MatchString(id) }

type Reference struct {
	SchemaVersion  int    `json:"schema_version"`
	OwnerID        string `json:"owner_id"`
	DaemonID       string `json:"daemon_id"`
	ResolverID     string `json:"resolver_id"`
	RootRef        string `json:"root_ref"`
	RootGeneration int    `json:"root_generation"`
	AuthorityID    string `json:"authority_id"`
	SourceID       string `json:"source_id"`
	SourceRevision int    `json:"source_revision"`
	EntryID        string `json:"entry_id"`
	ContentHash    string `json:"content_hash"`
	Format         string `json:"format"`
	EntryPoint     string `json:"entry_point"`
}

type Binding struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Reference Reference `json:"reference"`
}

type Selection struct {
	SchemaVersion int       `json:"schema_version"`
	SnapshotID    string    `json:"snapshot_id"`
	WorkspaceID   string    `json:"workspace_id"`
	ProjectID     string    `json:"project_id"`
	OwnerID       string    `json:"owner_id"`
	DaemonID      string    `json:"daemon_id"`
	Bindings      []Binding `json:"bindings"`
	Digest        string    `json:"digest"`
}

type TextFile struct {
	Path      string `json:"path"`
	Mode      string `json:"mode"`
	SizeBytes int    `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	Content   string `json:"content"`
}

type Entry struct {
	Binding Binding    `json:"binding"`
	Files   []TextFile `json:"files"`
}

type Snapshot struct {
	Selection Selection `json:"selection"`
	Entries   []Entry   `json:"entries"`
	Digest    string    `json:"digest"`
}

func DecodeReference(kind string, raw []byte) (Reference, error) {
	var ref Reference
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ref); err != nil {
		return ref, errors.New("invalid knowledge reference")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return ref, errors.New("invalid knowledge reference")
	}
	return ref, ref.Validate(kind)
}

func (r Reference) Validate(kind string) error {
	if !IsType(kind) || r.SchemaVersion != 1 || r.RootGeneration < 1 || r.SourceRevision < 1 || r.ResolverID != r.DaemonID {
		return errors.New("unsupported knowledge reference identity")
	}
	for _, id := range []string{r.OwnerID, r.DaemonID, r.ResolverID, r.RootRef, r.AuthorityID, r.SourceID, r.EntryID} {
		if !ValidID(id) {
			return errors.New("invalid knowledge identity")
		}
	}
	if !hashPattern.MatchString(r.ContentHash) {
		return errors.New("knowledge content hash is required")
	}
	if r.EntryID == r.SourceID || r.EntryID == r.RootRef {
		return errors.New("knowledge entry identity must be independent")
	}
	if kind == Skill {
		if r.Format != "native-skill-text-v1" || r.EntryPoint != "SKILL.md" {
			return errors.New("unsupported knowledge skill format")
		}
	} else if r.Format != "knowledge-text-v1" {
		return errors.New("unsupported knowledge text format")
	}
	if err := ValidatePath(r.EntryPoint); err != nil {
		return err
	}
	return nil
}

func ValidatePath(p string) error {
	if p == "" || len(p) > 1024 || !utf8.ValidString(p) || !norm.NFC.IsNormalString(p) || strings.ContainsAny(p, "\\:") || path.IsAbs(p) || len(strings.Split(p, "/")) > 16 {
		return errors.New("invalid knowledge relative path")
	}
	for _, c := range p {
		if unicode.IsControl(c) {
			return errors.New("invalid knowledge relative path")
		}
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." {
			return errors.New("invalid knowledge relative path")
		}
	}
	return nil
}

func Sum(data []byte) string      { h := sha256.Sum256(data); return "sha256:" + hex.EncodeToString(h[:]) }
func Frame(w io.Writer, s string) { fmt.Fprintf(w, "%d:%s\n", len([]byte(s)), s) }

func ContentHash(kind, format, entryPoint string, files []TextFile) (string, error) {
	sorted := append([]TextFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	if len(sorted) == 0 || len(sorted) > MaxFiles {
		return "", errors.New("knowledge file limit")
	}
	seen := map[string]bool{}
	total := 0
	primary := false
	h := sha256.New()
	domain := "knowledge-entry-v1"
	if !IsType(kind) {
		domain = "shared-library-entry-v1"
	}
	for _, s := range []string{domain, kind, format, entryPoint, strconv.Itoa(len(sorted))} {
		Frame(h, s)
	}
	for _, f := range sorted {
		if err := ValidatePath(f.Path); err != nil {
			return "", err
		}
		folded := cases.Fold().String(f.Path)
		if seen[folded] {
			return "", errors.New("knowledge path conflict")
		}
		seen[folded] = true
		if f.Mode != "text" || f.SizeBytes != len([]byte(f.Content)) || f.SizeBytes > MaxFileBytes || !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) || f.SHA256 != Sum([]byte(f.Content)) {
			return "", errors.New("invalid knowledge text file")
		}
		total += f.SizeBytes
		if total > MaxBytes {
			return "", errors.New("knowledge byte limit")
		}
		primary = primary || f.Path == entryPoint
		for _, s := range []string{f.Path, f.Mode, strconv.Itoa(f.SizeBytes), f.SHA256} {
			Frame(h, s)
		}
	}
	if !primary {
		return "", errors.New("knowledge entry point is missing")
	}
	Frame(h, "0")
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func (s Selection) Validate() error {
	if s.SchemaVersion != 1 || !ValidID(s.SnapshotID) || !ValidID(s.WorkspaceID) || (s.ProjectID != "" && !ValidID(s.ProjectID)) || !ValidID(s.OwnerID) || !ValidID(s.DaemonID) || len(s.Bindings) > MaxBindings {
		return errors.New("invalid knowledge selection")
	}
	seen := map[string]bool{}
	for _, b := range s.Bindings {
		if !ValidID(b.ID) || seen[b.ID] {
			return errors.New("invalid knowledge binding")
		}
		seen[b.ID] = true
		if err := b.Reference.Validate(b.Kind); err != nil {
			return err
		}
		if b.Reference.OwnerID != s.OwnerID || b.Reference.DaemonID != s.DaemonID {
			return errors.New("knowledge owner machine mismatch")
		}
	}
	expected := s.Seal()
	if expected.Digest != s.Digest {
		return errors.New("knowledge selection digest mismatch")
	}
	return nil
}

func (s Selection) Seal() Selection {
	s.Digest = ""
	raw, _ := json.Marshal(s)
	s.Digest = Sum(append([]byte("knowledge-selection-v1\n"), raw...))
	return s
}
func (s Snapshot) Seal() Snapshot {
	s.Digest = ""
	raw, _ := json.Marshal(s)
	s.Digest = Sum(append([]byte("knowledge-snapshot-v1\n"), raw...))
	return s
}
func (s Snapshot) Validate() error {
	if err := s.Selection.Validate(); err != nil {
		return err
	}
	if s.Seal().Digest != s.Digest || len(s.Entries) != len(s.Selection.Bindings) {
		return errors.New("invalid knowledge snapshot")
	}
	total := 0
	count := 0
	for i, e := range s.Entries {
		if e.Binding != s.Selection.Bindings[i] {
			return errors.New("knowledge snapshot binding mismatch")
		}
		kind := e.Binding.Kind
		if kind == Skill {
			kind = "skill"
		}
		hash, err := ContentHash(kind, e.Binding.Reference.Format, e.Binding.Reference.EntryPoint, e.Files)
		if err != nil {
			return err
		}
		if hash != e.Binding.Reference.ContentHash {
			return errors.New("knowledge content changed")
		}
		for _, f := range e.Files {
			total += f.SizeBytes
			count++
		}
	}
	if total > MaxBytes || count > 1024 {
		return errors.New("knowledge session limit")
	}
	return nil
}
