package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type LocalStore struct{ Dir string }
type Scope struct{ Server, WorkspaceID, OwnerID, DaemonID string }
type registration struct {
	Scope     Scope     `json:"scope"`
	Kind      string    `json:"kind"`
	Reference Reference `json:"reference"`
	Path      string    `json:"path"`
	Identity  string    `json:"identity"`
}

func (s LocalStore) Register(ctx context.Context, scope Scope, kind, locator string, allowRead, approveContext bool) (Reference, error) {
	if !allowRead || !approveContext {
		return Reference{}, errors.New("knowledge inspection and context approval are required")
	}
	if !IsType(kind) || scope.Server == "" || !ValidID(scope.WorkspaceID) || !ValidID(scope.OwnerID) || !ValidID(scope.DaemonID) {
		return Reference{}, errors.New("invalid knowledge registration scope")
	}
	if err := os.MkdirAll(filepath.Join(s.Dir, "sources"), 0700); err != nil {
		return Reference{}, err
	}
	lock, err := os.OpenFile(filepath.Join(s.Dir, "registration.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Reference{}, err
	}
	defer lock.Close()
	if err := lockRegistration(lock); err != nil {
		return Reference{}, errors.New("knowledge registration busy")
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	abs, err := filepath.Abs(locator)
	if err != nil {
		return Reference{}, errors.New("invalid knowledge locator")
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Reference{}, errors.New("knowledge source unavailable")
	}
	if canonical != abs {
		return Reference{}, errors.New("knowledge source must be a canonical non-symlink path")
	}
	ref := Reference{SchemaVersion: 1, OwnerID: scope.OwnerID, DaemonID: scope.DaemonID, ResolverID: scope.DaemonID, RootRef: uuid.NewString(), RootGeneration: 1, AuthorityID: "mu_local_" + scope.DaemonID, SourceID: uuid.NewString(), SourceRevision: 1, EntryID: uuid.NewString(), Format: "knowledge-text-v1", EntryPoint: filepath.Base(abs)}
	if kind == Folder {
		ref.EntryPoint = ""
	}
	if kind == Skill {
		ref.Format = "native-skill-text-v1"
		ref.EntryPoint = "SKILL.md"
	}
	var previous *registration
	registry, err := os.Open(filepath.Join(s.Dir, "sources"))
	if err != nil {
		return Reference{}, err
	}
	defer registry.Close()
	roots, err := registry.ReadDir(MaxNodes + 1)
	if err != nil && err != io.EOF {
		return Reference{}, err
	}
	if len(roots) > MaxNodes {
		return Reference{}, errors.New("knowledge registry limit")
	}
	ownerRoots := 0
	for _, root := range roots {
		if strings.HasPrefix(root.Name(), ".") {
			continue
		}
		if !strings.HasSuffix(root.Name(), ".json") {
			return Reference{}, errors.New("knowledge registry invalid")
		}
		if ctx.Err() != nil {
			return Reference{}, errors.New("knowledge resolution timeout")
		}
		raw, err := boundedLocalRead(filepath.Join(s.Dir, "sources", root.Name()), 16<<10)
		if err != nil {
			return Reference{}, errors.New("knowledge registry unavailable")
		}
		var saved registration
		if json.Unmarshal(raw, &saved) != nil {
			return Reference{}, errors.New("knowledge registry invalid")
		}
		if saved.Scope.OwnerID == scope.OwnerID {
			ownerRoots++
		}
		if saved.Scope == scope && saved.Path == abs && saved.Kind == kind {
			candidate := saved
			previous = &candidate
		}
	}
	if previous == nil && ownerRoots >= MaxBindings {
		return Reference{}, errors.New("knowledge source registration limit")
	}

	record := registration{Scope: scope, Kind: kind, Reference: ref, Path: abs}
	files, identity, err := readSource(ctx, record)
	if err != nil {
		return Reference{}, err
	}
	if kind == Folder {
		ref.EntryPoint = files[0].Path
	}
	hashKind := kind
	if kind == Skill {
		hashKind = "skill"
	}
	ref.ContentHash, err = ContentHash(hashKind, ref.Format, ref.EntryPoint, files)
	if err != nil {
		return Reference{}, err
	}
	if previous != nil {
		ref.RootRef = previous.Reference.RootRef
		ref.SourceID = previous.Reference.SourceID
		ref.EntryID = previous.Reference.EntryID
		ref.RootGeneration = previous.Reference.RootGeneration
		ref.SourceRevision = previous.Reference.SourceRevision
		if identity != previous.Identity {
			ref.RootGeneration++
			ref.SourceRevision++
		} else if ref.ContentHash != previous.Reference.ContentHash {
			ref.SourceRevision++
		}
	}

	record.Reference = ref
	record.Identity = identity
	raw, _ := json.Marshal(record)
	target := filepath.Join(s.Dir, "sources", ref.RootRef+".json")
	if previous == nil {
		if err := publish(target, raw); err != nil {
			return Reference{}, err
		}
	} else {
		tmp, err := os.CreateTemp(filepath.Dir(target), ".revision-*")
		if err != nil {
			return Reference{}, err
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.Write(raw); err != nil {
			tmp.Close()
			return Reference{}, err
		}
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return Reference{}, err
		}
		if err := tmp.Close(); err != nil {
			return Reference{}, err
		}
		if err := os.Rename(tmp.Name(), target); err != nil {
			return Reference{}, err
		}
	}

	return ref, nil
}

func (s LocalStore) Resolve(ctx context.Context, scope Scope, selection Selection) (Snapshot, error) {
	if err := selection.Validate(); err != nil {
		return Snapshot{}, err
	}
	if selection.WorkspaceID != scope.WorkspaceID || selection.OwnerID != scope.OwnerID || selection.DaemonID != scope.DaemonID {
		return Snapshot{}, errors.New("knowledge owner machine mismatch")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir := filepath.Join(s.Dir, "snapshots", Sum([]byte(scope.Server))[7:])
	target := filepath.Join(dir, selection.SnapshotID+".json")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Snapshot{}, err
	}
	lock, err := os.OpenFile(target+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Snapshot{}, err
	}
	defer lock.Close()
	for lockRegistration(lock) != nil {
		select {
		case <-ctx.Done():
			return Snapshot{}, errors.New("knowledge resolution timeout")
		case <-time.After(10 * time.Millisecond):
		}
	}
	receipt := target + ".committed"
	committed, receiptErr := boundedLocalRead(receipt, 128)
	if receiptErr == nil && string(committed) != selection.Digest {
		return Snapshot{}, errors.New("knowledge snapshot receipt mismatch")
	}
	if receiptErr != nil && !errors.Is(receiptErr, os.ErrNotExist) {
		return Snapshot{}, errors.New("knowledge snapshot receipt unavailable")
	}

	raw, err := boundedLocalRead(target, MaxBytes*8)
	if err == nil {
		var snapshot Snapshot
		if json.Unmarshal(raw, &snapshot) != nil || snapshot.Selection.Digest != selection.Digest {
			return Snapshot{}, errors.New("knowledge retained snapshot identity mismatch")
		}
		if err := snapshot.Validate(); err != nil {
			return Snapshot{}, err
		}
		if receiptErr != nil {
			if err := publish(receipt, []byte(selection.Digest)); err != nil {
				return Snapshot{}, err
			}
		}
		return snapshot, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, errors.New("knowledge retained snapshot unavailable")
	}
	if receiptErr == nil {
		return Snapshot{}, errors.New("knowledge retained snapshot is missing; live sources will not be re-resolved")
	}
	snapshot := Snapshot{Selection: selection, Entries: []Entry{}}
	total, count := 0, 0
	for _, binding := range selection.Bindings {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, errors.New("knowledge resolution timeout")
		}
		raw, err := boundedLocalRead(filepath.Join(s.Dir, "sources", binding.Reference.RootRef+".json"), 16<<10)
		if err != nil {
			return Snapshot{}, errors.New("knowledge source unavailable on owner machine")
		}
		var record registration
		if json.Unmarshal(raw, &record) != nil || record.Scope != scope || record.Kind != binding.Kind || record.Reference != binding.Reference {
			return Snapshot{}, errors.New("knowledge source authorization mismatch")
		}
		files, identity, err := readSource(ctx, record)
		if err != nil {
			return Snapshot{}, err
		}
		if identity != record.Identity {
			return Snapshot{}, errors.New("knowledge root replaced; register and approve a new reference")
		}
		snapshot.Entries = append(snapshot.Entries, Entry{Binding: binding, Files: files})
		for _, f := range files {
			total += f.SizeBytes
			count++
			if total > MaxBytes || count > 1024 {
				return Snapshot{}, errors.New("knowledge session limit")
			}
		}
	}
	snapshot = snapshot.Seal()
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Snapshot{}, err
	}
	raw, err = json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	if ctx.Err() != nil {
		return Snapshot{}, errors.New("knowledge resolution timeout")
	}
	if err := publish(receipt, []byte(selection.Digest)); err != nil {
		return Snapshot{}, err
	}
	if err := publish(target, raw); err != nil {
		return Snapshot{}, err
	}

	return snapshot, nil
}

func publish(target string, raw []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(target), ".knowledge-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Link(temp.Name(), target)
}

func boundedLocalRead(path string, limit int64) ([]byte, error) {
	f, err := openLocal(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("knowledge storage is not a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("knowledge storage limit")
	}
	return raw, nil
}

func readSource(ctx context.Context, record registration) ([]TextFile, string, error) {
	return readSourceBounded(ctx, record, MaxBytes, MaxFiles)
}

func readSourceBounded(ctx context.Context, record registration, byteLimit, fileLimit int) ([]TextFile, string, error) {
	canonical, err := filepath.EvalSymlinks(record.Path)
	if err != nil || canonical != record.Path {
		return nil, "", errors.New("knowledge source unavailable or symlink")
	}
	sourceInfo, err := os.Lstat(record.Path)
	if err != nil || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return nil, "", errors.New("knowledge source unavailable or symlink")
	}
	rootPath := record.Path
	name := "."
	if record.Kind == File {
		rootPath = filepath.Dir(record.Path)
		name = filepath.Base(record.Path)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, "", errors.New("knowledge source unavailable")
	}
	defer root.Close()
	f, err := root.OpenFile(name, readFlags(), 0)
	if err != nil {
		return nil, "", errors.New("knowledge source unavailable or symlink")
	}
	identity, err := fileIdentity(f)
	selectedInfo, statErr := f.Stat()
	f.Close()
	if statErr != nil || !os.SameFile(sourceInfo, selectedInfo) {
		return nil, "", errors.New("knowledge root changed while opening")
	}
	if err != nil {
		return nil, "", errors.New("knowledge source identity unavailable")
	}
	if record.Identity != "" && record.Identity != identity {
		return nil, "", errors.New("knowledge root replaced")
	}
	var files []TextFile
	total, nodes := 0, 0
	var visit func(string, int) error
	visit = func(rel string, depth int) error {
		if err := ctx.Err(); err != nil {
			return errors.New("knowledge resolution timeout")
		}
		nodes++
		if nodes > MaxNodes || depth > MaxDepth {
			return errors.New("knowledge scan limit")
		}
		before, err := root.Lstat(rel)
		if err == nil && rel == name && !os.SameFile(selectedInfo, before) {
			return errors.New("knowledge root replaced while reading")
		}
		if err != nil {
			return errors.New("knowledge source unavailable")
		}
		if before.Mode()&os.ModeSymlink != 0 || (!before.Mode().IsRegular() && !before.IsDir()) {
			return errors.New("knowledge symlink is unsupported")
		}
		opened, err := root.OpenFile(rel, readFlags(), 0)
		if err != nil {
			return errors.New("knowledge source unavailable or symlink")
		}
		defer opened.Close()
		info, err := opened.Stat()
		if err != nil || !os.SameFile(before, info) {
			return errors.New("knowledge source changed while reading")
		}
		if info.IsDir() {
			if record.Kind == File {
				return errors.New("knowledge file reference is a directory")
			}
			children, err := opened.ReadDir(MaxNodes + 1)
			if err != nil && err != io.EOF {
				return errors.New("knowledge directory read failed")
			}
			if len(children) > MaxNodes {
				return errors.New("knowledge scan limit")
			}
			for _, child := range children {
				if err := visit(filepath.Join(rel, child.Name()), depth+1); err != nil {
					return err
				}
			}
		} else {
			if !info.Mode().IsRegular() || info.Mode()&0111 != 0 {
				return errors.New("knowledge special or executable file is unsupported")
			}
			if record.Kind != File && rel == "." {
				return errors.New("knowledge folder reference is a file")
			}
			if len(files) >= fileLimit {
				return errors.New("knowledge file limit")
			}
			raw, err := io.ReadAll(io.LimitReader(opened, int64(min(MaxFileBytes, byteLimit-total))+1))
			if ctx.Err() != nil {
				return errors.New("knowledge resolution timeout")
			}
			if err != nil {
				return errors.New("knowledge file read failed")
			}
			if len(raw) > MaxFileBytes {
				return errors.New("knowledge file limit")
			}
			after, err := opened.Stat()
			if err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
				return errors.New("knowledge source changed while reading")
			}
			total += len(raw)
			if total > byteLimit {
				return errors.New("knowledge byte limit")
			}
			p := filepath.ToSlash(rel)
			if record.Kind == File {
				p = name
			}
			files = append(files, TextFile{Path: p, Mode: "text", SizeBytes: len(raw), SHA256: Sum(raw), Content: string(raw)})
		}
		after, err := root.Lstat(rel)
		if err != nil || !os.SameFile(info, after) {
			return errors.New("knowledge source replaced while reading")
		}
		return nil
	}
	if err := visit(name, 0); err != nil {
		return nil, "", err
	}
	if len(files) == 0 {
		return nil, "", errors.New("knowledge source is empty")
	}
	current, err := os.Lstat(record.Path)
	canonical, canonicalErr := filepath.EvalSymlinks(record.Path)
	if err != nil || canonicalErr != nil || canonical != record.Path || !os.SameFile(sourceInfo, current) {
		return nil, "", errors.New("knowledge root replaced while reading")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, identity, nil
}
