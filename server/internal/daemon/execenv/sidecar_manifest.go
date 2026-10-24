package execenv

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// sidecarManifestFile is the on-disk JSON Prepare writes into envRoot to
// record every file and intermediate directory it created inside WorkDir.
// CleanupSidecars reads it back to roll the workdir to its pre-Prepare
// state. The file lives in envRoot (daemon scratch), never in WorkDir,
// so a local_directory run does not litter the user's repo with the
// bookkeeping file used to undo the litter.
const sidecarManifestFile = ".multica_sidecar_manifest.json"

// errPathPreExists is the sentinel recordWriteFile returns when the
// target path already exists. The manifest contract is that we never
// mutate paths we don't own: a pre-existing file belongs to the user
// (or to stale state from a crashed prior run we cannot safely
// distinguish from intentional user content) and the write must be
// refused so cleanup can be a pure deletion of paths we created.
//
// Callers handle this in one of two ways:
//
//   - For per-skill directories the caller allocates a collision-free
//     alternative slug (see allocateCollisionFreeSkillDir) and retries
//     so the agent still discovers the Multica skill, just under a
//     different directory name.
//   - For the Multica-only namespace (.multica/project/resources.json)
//     the caller swallows the error and proceeds — the agent's runtime
//     brief already carries every fact that would have appeared in that
//     file, so missing-from-disk is degraded behavior for resources. Tasks
//     with pinned knowledge fail preparation because they need those bytes.
var errPathPreExists = errors.New("execenv: refuse to overwrite pre-existing path")

// Path-only records remain readable but cannot establish ownership.
type sidecarManifest struct {
	Version      int                         `json:"version,omitempty"`
	Generation   string                      `json:"generation,omitempty"`
	Files        []string                    `json:"files,omitempty"`
	Dirs         []string                    `json:"dirs,omitempty"`
	Owned        map[string]sidecarOwnership `json:"owned,omitempty"`
	Root         string                      `json:"root,omitempty"`
	RootIdentity string                      `json:"rootIdentity,omitempty"`
}

type sidecarOwnership struct {
	Generation string            `json:"generation"`
	Identity   string            `json:"identity"`
	Parents    map[string]string `json:"parents"`
	Hash       string            `json:"sha256,omitempty"`
	Size       int64             `json:"size,omitempty"`
	Mode       uint32            `json:"mode,omitempty"`
	Modified   int64             `json:"modified,omitempty"`
}

func (m *sidecarManifest) initialize() error {
	if m.Version == 2 && m.Generation != "" && m.Owned != nil {
		return nil
	}
	if m.Version != 0 || len(m.Files)+len(m.Dirs) != 0 {
		return errors.New("legacy sidecar ownership is unverifiable")
	}
	var generation [32]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return err
	}
	m.Version, m.Generation, m.Owned = 2, hex.EncodeToString(generation[:]), make(map[string]sidecarOwnership)
	return nil
}

func sameSidecarPath(a, b string) bool {
	canonicalA, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	canonicalB, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	absoluteA, err := filepath.Abs(canonicalA)
	if err != nil {
		return false
	}
	absoluteB, err := filepath.Abs(canonicalB)
	return err == nil && absoluteA == absoluteB
}

func (m *sidecarManifest) bindRoot(path string) error {
	if m == nil {
		return nil
	}
	if err := m.initialize(); err != nil {
		return err
	}
	input, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(input)
	if err != nil {
		return err
	}
	f, err := os.Open(canonical)
	if err != nil {
		return err
	}
	defer f.Close()
	identity, err := sidecarFileIdentity(f)
	if err != nil {
		return err
	}
	if m.Root != "" && (!sameSidecarPath(m.Root, canonical) || m.RootIdentity != identity) {
		return errors.New("sidecar root changed")
	}
	m.Root, m.RootIdentity = canonical, identity
	return nil
}

func (m *sidecarManifest) parent(path string) (*os.Root, string, map[string]string, error) {
	root, canonical, parents, err := sidecarParent(path, nil)
	if err != nil {
		return nil, "", nil, err
	}
	if m.Root != "" {
		relative, err := filepath.Rel(m.Root, canonical)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || parents[m.Root] != m.RootIdentity {
			root.Close()
			return nil, "", nil, errors.New("sidecar escaped or replaced its workdir")
		}
	}
	return root, canonical, parents, nil
}

// Pin each ancestor through directory handles and reject links and replacements.
func sidecarParent(path string, expected map[string]string) (*os.Root, string, map[string]string, error) {
	parent := filepath.Dir(path)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, "", nil, err
	}
	if expected != nil && canonical != parent {
		return nil, "", nil, errors.New("sidecar ancestor became a symlink")
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, "", nil, err
	}
	volume := filepath.VolumeName(canonical) + string(filepath.Separator)
	root, err := os.OpenRoot(volume)
	if err != nil {
		return nil, "", nil, err
	}
	parents := make(map[string]string)
	current := volume
	parts := strings.Split(strings.TrimPrefix(canonical, volume), string(filepath.Separator))
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			if parts[i] == "" {
				continue
			}
			before, err := root.Lstat(parts[i])
			if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
				root.Close()
				return nil, "", nil, errors.New("unsafe sidecar ancestor")
			}
			child, err := root.OpenRoot(parts[i])
			if err != nil {
				root.Close()
				return nil, "", nil, err
			}
			opened, err := child.Stat(".")
			root.Close()
			if err != nil || !os.SameFile(before, opened) {
				child.Close()
				return nil, "", nil, errors.New("sidecar ancestor changed")
			}
			root = child
			current = filepath.Join(current, parts[i])
		}
		dir, err := root.Open(".")
		if err != nil {
			root.Close()
			return nil, "", nil, err
		}
		identity, err := sidecarFileIdentity(dir)
		dir.Close()
		if err != nil {
			root.Close()
			return nil, "", nil, err
		}
		if expected != nil && expected[current] != identity {
			root.Close()
			return nil, "", nil, errors.New("sidecar ancestor ownership changed")
		}
		parents[current] = identity
	}
	return root, filepath.Join(canonical, filepath.Base(path)), parents, nil
}

func recordMkdirAll(path string, perm os.FileMode, m *sidecarManifest) error {
	if m == nil || path == "" {
		return os.MkdirAll(path, perm)
	}
	if err := m.initialize(); err != nil {
		return err
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe sidecar directory")
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := recordMkdirAll(filepath.Dir(path), perm, m); err != nil {
		return err
	}
	root, canonical, parents, err := m.parent(path)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir(filepath.Base(canonical), perm); err != nil {
		return err
	}
	dir, err := root.Open(filepath.Base(canonical))
	if err != nil {
		return err
	}
	defer dir.Close()
	identity, err := sidecarFileIdentity(dir)
	if err != nil {
		return err
	}
	m.Dirs = append(m.Dirs, canonical)
	m.Owned[canonical] = sidecarOwnership{Generation: m.Generation, Identity: identity, Parents: parents}
	return nil
}

func recordWriteFile(path string, data []byte, perm os.FileMode, m *sidecarManifest) error {
	if m == nil {
		return os.WriteFile(path, data, perm)
	}
	if err := m.initialize(); err != nil {
		return err
	}
	root, canonical, parents, err := m.parent(path)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile(filepath.Base(canonical), os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: %s", errPathPreExists, path)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	identity, err := sidecarFileIdentity(f)
	if err != nil {
		return err
	}
	m.Files = append(m.Files, canonical)
	owned := sidecarOwnership{Generation: m.Generation, Identity: identity, Parents: parents}
	m.Owned[canonical] = owned
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	identity, err = sidecarFileIdentity(f)
	if err != nil {
		return err
	}
	owned.Identity = identity
	digest := sha256.Sum256(data)
	owned.Hash, owned.Size, owned.Mode, owned.Modified = hex.EncodeToString(digest[:]), info.Size(), uint32(info.Mode()), info.ModTime().UnixNano()
	m.Owned[canonical] = owned
	return nil
}

// allocateCollisionFreeSkillDir picks a directory under skillsParent
// whose path does NOT currently exist, so writeSkillFiles can lay
// down a Multica skill without colliding with a user-installed skill
// of the same slug. The first attempt is always the natural baseSlug
// — that's the path provider-native discovery already knows. On
// collision we append `-multica`, then `-multica-2`, `-multica-3`,
// … until a free slot is found. The chosen slug is returned alongside
// the absolute path so callers can use it in frontmatter and brief
// listings.
//
// The collision-free fallback name is still a sibling under the same
// skillsParent, so provider-native discovery still picks the skill up
// (each subdir under .claude/skills/ etc. is scanned independently).
// The user's directory at baseSlug is left bit-for-bit intact.
//
// The probe is bounded to a small ceiling — a user with thousands of
// collisions on the same slug indicates an upstream bug, not a
// realistic state. Returning an error in that case forces the caller
// to surface the problem instead of looping forever.
func allocateCollisionFreeSkillDir(skillsParent, baseSlug string) (slug, dir string, err error) {
	const maxAttempts = 64
	for i := 0; i < maxAttempts; i++ {
		candidate := skillSlugCandidate(baseSlug, i)
		path := filepath.Join(skillsParent, candidate)
		if _, statErr := os.Lstat(path); statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				return candidate, path, nil
			}
			return "", "", fmt.Errorf("stat candidate %s: %w", path, statErr)
		}
	}
	return "", "", fmt.Errorf("allocate collision-free skill dir under %s: exhausted %d attempts for base %q", skillsParent, maxAttempts, baseSlug)
}

// skillSlugCandidate is the nth name to try for a skill whose natural slug is
// baseSlug: the bare slug first, then `-multica`, then numbered variants.
//
// Two callers must agree on this sequence — allocateCollisionFreeSkillDir,
// which probes the filesystem, and resolveSkillSlugs, which deduplicates a
// batch in memory before anything is written. If they disagreed, a skill would
// be listed under one name and written under another.
func skillSlugCandidate(baseSlug string, attempt int) string {
	switch {
	case attempt <= 0:
		return baseSlug
	case attempt == 1:
		return baseSlug + "-multica"
	default:
		return fmt.Sprintf("%s-multica-%d", baseSlug, attempt)
	}
}

// writeSidecarManifest persists m to {envRoot}/{sidecarManifestFile}.
// Empty manifests are still written so a later Cleanup that finds the
// file knows tracking was attempted (vs. an old build that predates this
// mechanism, where the file is absent and Cleanup must no-op).
//
// Failures are returned to the caller, and how much they matter depends on
// where the sidecars landed. For a cloud envRoot the manifest is a convenience
// the GC can do without, so Prepare logs and continues. For an in-place
// local_directory run it is the only record of what was written into the user's
// own repository, so Prepare treats the failure as fatal and rolls back while
// it still holds the in-memory manifest (MUL-6132).
func writeSidecarManifest(envRoot string, m *sidecarManifest) error {
	if envRoot == "" {
		return nil
	}
	if m == nil {
		m = &sidecarManifest{}
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal sidecar manifest: %w", err)
	}
	root, err := os.OpenRoot(envRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := ".sidecars-" + hex.EncodeToString(nonce[:])
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return root.Rename(name, sidecarManifestFile)
}

// CleanupSidecars removes manifest-owned files, then directories deepest first.
// Missing paths are already clean. Changed or unverifiable entries remain and
// return conflicts; their manifest is retained for inspection and later cleanup.
//
// An empty envRoot or missing manifest is a no-op. Pair with CleanupRuntimeConfig,
// which restores the runtime brief; this function handles the sidecar tree.
func CleanupSidecars(envRoot string) error {
	if envRoot == "" {
		return nil
	}
	manifestPath := filepath.Join(envRoot, sidecarManifestFile)
	data, err := os.ReadFile(manifestPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read sidecar manifest %s: %w", manifestPath, err)
	}
	var m sidecarManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse sidecar manifest %s: %w", manifestPath, err)
	}

	return rollBackManifest(m, manifestPath)
}

func rollBackManifest(m sidecarManifest, manifestPath string) error {
	var conflicts []error
	remove := func(path string, directory bool) {
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			if owned, ok := m.Owned[path]; ok && m.Version == 2 && owned.Generation == m.Generation {
				if retained, err := retainedSidecarCapture(path, owned); err != nil {
					conflicts = append(conflicts, fmt.Errorf("retain %s: %w", path, err))
				} else if retained != "" {
					conflicts = append(conflicts, fmt.Errorf("captured entry retained at %s", retained))
				}
			}
			return
		}
		owned, ok := m.Owned[path]
		if m.Version != 2 || m.Generation == "" || !ok || owned.Generation != m.Generation || owned.Identity == "" || len(owned.Parents) == 0 {
			conflicts = append(conflicts, fmt.Errorf("retain %s: ownership is unverifiable", path))
			return
		}
		if err := removeOwnedSidecar(path, owned, directory); err != nil {
			conflicts = append(conflicts, fmt.Errorf("retain %s: %w", path, err))
		}
	}
	for _, path := range m.Files {
		remove(path, false)
	}
	for i := len(m.Dirs) - 1; i >= 0; i-- {
		remove(m.Dirs[i], true)
	}
	if len(conflicts) != 0 {
		return errors.Join(conflicts...)
	}
	if manifestPath != "" {
		if err := os.Remove(manifestPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// A retained capture remains a conflict even after the public path disappears.
// Inspect names in bounded batches; never adopt or delete quarantine contents.
func retainedSidecarCapture(path string, owned sidecarOwnership) (string, error) {
	root, _, _, err := sidecarParent(path, owned.Parents)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer dir.Close()
	for {
		names, err := dir.Readdirnames(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		for _, name := range names {
			if !strings.HasPrefix(name, ".multica-cleanup-") {
				continue
			}
			candidate := filepath.Join(name, filepath.Base(path))
			if _, statErr := root.Lstat(candidate); statErr == nil {
				return filepath.Join(root.Name(), candidate), nil
			} else if !errors.Is(statErr, fs.ErrNotExist) {
				return "", statErr
			}
		}
		if errors.Is(err, io.EOF) {
			return "", nil
		}
	}
}

func removeOwnedSidecar(path string, owned sidecarOwnership, directory bool) error {
	return removeOwnedSidecarWithCaptureHooks(path, owned, directory, nil, nil)
}

func removeOwnedSidecarWithCaptureHooks(path string, owned sidecarOwnership, directory bool, beforeCapture, afterCapture func()) error {
	root, canonical, _, err := sidecarParent(path, owned.Parents)
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(canonical)
	before, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if before.Mode()&os.ModeSymlink != 0 || before.IsDir() != directory || (!directory && !before.Mode().IsRegular()) {
		return errors.New("entry type changed")
	}
	f, err := root.OpenFile(name, sidecarReadFlags(), 0)
	if err != nil {
		return err
	}
	defer f.Close()
	identity, err := sidecarFileIdentity(f)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if identity != owned.Identity || !os.SameFile(before, info) {
		return errors.New("entry ownership changed")
	}
	if !directory {
		if owned.Hash == "" || info.Size() != owned.Size || uint32(info.Mode()) != owned.Mode || info.ModTime().UnixNano() != owned.Modified {
			return errors.New("baseline changed")
		}
		digest := sha256.New()
		n, err := io.Copy(digest, io.LimitReader(f, owned.Size+1))
		if err != nil {
			return err
		}
		if n != owned.Size || hex.EncodeToString(digest.Sum(nil)) != owned.Hash {
			return errors.New("baseline hash changed")
		}
		after, err := f.Stat()
		if err != nil || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
			return errors.New("entry changed during verification")
		}
	}
	finalIdentity, err := sidecarFileIdentity(f)
	if err != nil || finalIdentity != owned.Identity {
		return errors.New("entry ownership changed during verification")
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return errors.New("entry replaced during verification")
	}
	if directory {
		entries, err := f.ReadDir(1)
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read owned directory: %w", err)
		}
		if len(entries) != 0 {
			return errors.New("owned directory is not empty")
		}
	}
	if beforeCapture != nil {
		beforeCapture()
	}
	return captureAndRemoveOwnedSidecar(root, name, info, owned, directory, afterCapture)
}

// Rename captures the leaf atomically through the pinned parent. Verification
// and deletion below address only this private candidate, never the public name.
func captureAndRemoveOwnedSidecar(root *os.Root, name string, expected os.FileInfo, owned sidecarOwnership, directory bool, afterCapture func()) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	quarantine := ".multica-cleanup-" + hex.EncodeToString(nonce[:])
	if err := root.Mkdir(quarantine, 0700); err != nil {
		return err
	}
	captured, err := root.OpenRoot(quarantine)
	if err != nil {
		root.Remove(quarantine)
		return err
	}
	defer captured.Close()
	leaf := name
	if err := root.Rename(name, filepath.Join(quarantine, leaf)); err != nil {
		root.Remove(quarantine)
		return err
	}
	if afterCapture != nil {
		afterCapture()
	}
	retainedPath := filepath.Join(root.Name(), quarantine, leaf)
	retain := func(cause error) error {
		// Link refuses to overwrite an editor's newly created public name. If the
		// captured leaf cannot be restored, retain it with its recovery location.
		if !directory {
			if err := root.Link(filepath.Join(quarantine, leaf), name); err == nil {
				if err := captured.Remove(leaf); err != nil {
					return fmt.Errorf("%w; restored entry, extra captured link retained at %s: %v", cause, retainedPath, err)
				}
				root.Remove(quarantine)
				return fmt.Errorf("%w; captured entry restored", cause)
			}
		}
		return fmt.Errorf("%w; captured entry retained at %s", cause, retainedPath)
	}
	info, err := captured.Lstat(leaf)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || !os.SameFile(expected, info) {
		return retain(errors.New("entry replaced during capture"))
	}
	f, err := captured.OpenFile(leaf, sidecarReadFlags(), 0)
	if err != nil {
		return retain(err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(expected, opened) {
		return retain(errors.New("captured entry ownership changed"))
	}
	// A rename can change ctime. Compare stable file identity to the original
	// handle and compare the captured revision before/after reading its bytes.
	revision, err := sidecarFileIdentity(f)
	if err != nil {
		return retain(err)
	}
	if !directory {
		if opened.Size() != owned.Size || uint32(opened.Mode()) != owned.Mode || opened.ModTime().UnixNano() != owned.Modified {
			return retain(errors.New("captured baseline changed"))
		}
		digest := sha256.New()
		n, err := io.Copy(digest, io.LimitReader(f, owned.Size+1))
		if err != nil {
			return retain(err)
		}
		if n != owned.Size || hex.EncodeToString(digest.Sum(nil)) != owned.Hash {
			return retain(errors.New("captured baseline hash changed"))
		}
	}
	finalRevision, err := sidecarFileIdentity(f)
	if err != nil || finalRevision != revision {
		return retain(errors.New("captured entry changed during verification"))
	}
	if err := captured.Remove(leaf); err != nil {
		return retain(err)
	}
	if err := root.Remove(quarantine); err != nil {
		return err
	}
	if _, err := root.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
		return errors.New("entry recreated during cleanup; replacement retained")
	}
	return nil
}

// rollBackPreparedSidecars undoes the sidecar writes of a Prepare that failed
// before it could persist the manifest, using the in-memory manifest Prepare
// was still filling in.
//
// This is the only rollback available on that path. Prepare writes the daemon
// task marker and the rest of the sidecar tree into the workdir early, but only
// persists the manifest at the very end; every error return in between leaves
// that tree on disk with no on-disk record of it, and the caller never receives
// an Environment, so no teardown defer downstream knows there is anything to
// clean (MUL-6132). For a local_directory task the workdir is the user's own
// repository, so "left on disk" means a marker that disables every multica
// command in that directory tree until someone deletes it by hand.
//
// There is no manifest file to remove, so manifestPath is empty.
func rollBackPreparedSidecars(m sidecarManifest) error {
	return rollBackManifest(m, "")
}
