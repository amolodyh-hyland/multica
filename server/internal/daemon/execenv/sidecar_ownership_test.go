package execenv

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSidecarOwnershipCleanup(t *testing.T) {
	for _, scenario := range []string{"unchanged", "edited", "hash", "replaced", "generation", "legacy", "directory-replaced", "symlink", "added"} {
		t.Run(scenario, func(t *testing.T) {
			workDir, envRoot := t.TempDir(), t.TempDir()
			dir := filepath.Join(workDir, "skill")
			path := filepath.Join(dir, "SKILL.md")
			m := &sidecarManifest{}
			if err := recordMkdirAll(dir, 0700, m); err != nil {
				t.Fatal(err)
			}
			if err := recordWriteFile(path, []byte("original"), 0600, m); err != nil {
				t.Fatal(err)
			}
			// Resolve the same canonical path used by ownership records.
			path = m.Files[0]
			dir = filepath.Dir(path)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "edited", "hash":
				if err := os.WriteFile(path, []byte("modified"), 0600); err != nil {
					t.Fatal(err)
				}
				if scenario == "hash" {
					if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
						t.Fatal(err)
					}
				}
			case "replaced":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
			case "generation":
				m.Generation = "different-generation"
			case "legacy":
				m.Version = 0
				m.Generation = ""
				m.Owned = nil
			case "directory-replaced", "symlink":
				if err := os.Rename(dir, dir+".old"); err != nil {
					t.Fatal(err)
				}
				if scenario == "symlink" {
					if err := os.Symlink(dir+".old", dir); err != nil {
						t.Skipf("symlinks unavailable: %v", err)
					}
				} else {
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "added":
				if err := os.WriteFile(filepath.Join(dir, "notes"), []byte("user"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeSidecarManifest(envRoot, m); err != nil {
				t.Fatal(err)
			}
			err = CleanupSidecars(envRoot)
			if scenario == "unchanged" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("owned empty directory remains: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("missing retained cleanup conflict")
			}
			if _, err := os.Stat(filepath.Join(envRoot, sidecarManifestFile)); err != nil {
				t.Fatalf("conflict evidence lost: %v", err)
			}
			target := path
			if scenario == "added" {
				target = filepath.Join(dir, "notes")
			}
			if _, err := os.Stat(target); err != nil {
				t.Fatalf("unverified/user content lost: %v", err)
			}
			// Rollback uses the same checks as persisted cleanup.
			if err := rollBackPreparedSidecars(*m); err == nil {
				t.Fatal("rollback did not report retained conflict")
			}
			if _, err := os.Stat(target); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSidecarWriteRefusesExistingMarkerChanges(t *testing.T) {
	dir := t.TempDir()
	original := TaskContextForEnv{AgentID: "one"}
	if err := writeTaskContextMarker(dir, original, &sidecarManifest{}); err != nil {
		t.Fatal(err)
	}
	if err := writeTaskContextMarker(dir, TaskContextForEnv{AgentID: "two"}, &sidecarManifest{}); err == nil {
		t.Fatal("unverified existing marker overwritten")
	}
}

func TestSidecarOwnershipCannotBeRestoredWithModificationTimestamp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix change-time ownership check")
	}
	path := filepath.Join(t.TempDir(), "owned")
	manifest := &sidecarManifest{}
	if err := recordWriteFile(path, []byte("baseline"), 0600, manifest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := rollBackPreparedSidecars(*manifest); err == nil {
		t.Fatal("timestamp restoration forged original ownership")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "baseline" {
		t.Fatal("revised file removed")
	}
}

func TestSidecarManifestRestrictsPrivatePathMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions")
	}
	root := t.TempDir()
	path := filepath.Join(root, sidecarManifestFile)
	if err := os.WriteFile(path, []byte(`{"files":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeSidecarManifest(root, &sidecarManifest{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private sidecar paths are readable by others: %v, %v", info, err)
	}
}
