package execenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestSidecarCleanupCapturesFinalReplacementWindow(t *testing.T) {
	for _, scenario := range []string{"replace before capture", "edit before capture", "replace after capture", "open writer after capture"} {
		t.Run(scenario, func(t *testing.T) {
			workDir, envRoot := t.TempDir(), t.TempDir()
			path := filepath.Join(workDir, "owned")
			m := &sidecarManifest{}
			if err := recordWriteFile(path, []byte("baseline"), 0600, m); err != nil {
				t.Fatal(err)
			}
			path = m.Files[0]
			if err := writeSidecarManifest(envRoot, m); err != nil {
				t.Fatal(err)
			}
			var writer *os.File
			if scenario == "open writer after capture" {
				var err error
				writer, err = os.OpenFile(path, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
			}
			edit := func() {
				if scenario == "edit before capture" {
					if err := os.WriteFile(path, []byte("useredit"), 0600); err != nil {
						t.Fatal(err)
					}
					return
				}
				if scenario == "open writer after capture" {
					if _, err := writer.WriteAt([]byte("useredit"), 0); err != nil {
						t.Fatal(err)
					}
					return
				}
				temp := filepath.Join(workDir, "editor-save")
				if err := os.WriteFile(temp, []byte("useredit"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(temp, path); err != nil {
					t.Fatal(err)
				}
			}
			var before, after func()
			if scenario == "replace before capture" || scenario == "edit before capture" {
				before = edit
			} else {
				after = edit
			}
			if err := removeOwnedSidecarWithCaptureHooks(path, m.Owned[path], false, before, after); err == nil {
				t.Fatal("final-window change did not return a conflict")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "useredit" {
				t.Fatalf("user edit was deleted or not restored: %q %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(envRoot, sidecarManifestFile)); err != nil {
				t.Fatal("conflict manifest lost")
			}
			if err := CleanupSidecars(envRoot); err == nil {
				t.Fatal("repeated cleanup adopted the replacement")
			}
			data, err = os.ReadFile(path)
			if err != nil || string(data) != "useredit" {
				t.Fatal("repeated cleanup lost user edit")
			}
		})
	}
}

func TestSidecarCleanupLeavesCapturedConflictWhenPublicNameReappears(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owned")
	m := &sidecarManifest{}
	if err := recordWriteFile(path, []byte("baseline"), 0600, m); err != nil {
		t.Fatal(err)
	}
	path = m.Files[0]
	root = filepath.Dir(path)
	err := removeOwnedSidecarWithCaptureHooks(path, m.Owned[path], false, func() {
		if err := os.WriteFile(path, []byte("captured user edit"), 0600); err != nil {
			t.Fatal(err)
		}
	}, func() {
		if err := os.WriteFile(path, []byte("new public edit"), 0600); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil {
		t.Fatal("captured conflict not reported")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "new public edit" {
		t.Fatal("restoration overwrote public replacement")
	}
	matches, globErr := filepath.Glob(filepath.Join(root, ".multica-cleanup-*", filepath.Base(path)))
	if globErr != nil || len(matches) != 1 {
		t.Fatalf("captured edit not retained: %v %v", matches, globErr)
	}
	data, readErr = os.ReadFile(matches[0])
	if readErr != nil || string(data) != "captured user edit" {
		t.Fatal("captured user edit lost")
	}
	if !strings.Contains(err.Error(), matches[0]) {
		t.Fatalf("recovery location absent from conflict: %v", err)
	}
	envRoot := t.TempDir()
	if err := writeSidecarManifest(envRoot, m); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := CleanupSidecars(envRoot); err == nil {
		t.Fatal("missing public path concealed retained capture")
	}
	if _, err := os.Stat(filepath.Join(envRoot, sidecarManifestFile)); err != nil {
		t.Fatal("retained capture lost its manifest")
	}
}
