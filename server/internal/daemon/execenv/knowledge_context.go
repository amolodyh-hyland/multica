package execenv

import (
	"errors"
	"os"
	"path/filepath"
)

func writeKnowledgeProjectContext(workDir string, data []byte, manifest *sidecarManifest) error {
	if manifest != nil {
		if err := manifest.bindRoot(workDir); err != nil {
			return err
		}
		dir := filepath.Join(workDir, ".multica", "project")
		if err := recordMkdirAll(dir, 0700, manifest); err != nil {
			return err
		}
		return recordWriteFile(filepath.Join(dir, "resources.json"), data, 0600, manifest)
	}

	root, err := os.OpenRoot(workDir)
	if err != nil {
		return err
	}
	defer func() { root.Close() }()
	relative := ""
	for _, part := range []string{".multica", "project"} {
		relative = filepath.Join(relative, part)
		before, err := root.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(part, 0700); err != nil {
				return err
			}
			before, err = root.Lstat(part)
		}
		if err != nil {
			return err
		}
		if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			return errors.New("knowledge context directory is unsafe")
		}
		child, err := root.OpenRoot(part)
		if err != nil {
			return err
		}
		opened, err := child.Stat(".")
		if err != nil || !os.SameFile(before, opened) {
			child.Close()
			return errors.New("knowledge context directory changed")
		}
		root.Close()
		root = child
	}
	const name = "resources.json"
	before, err := root.Lstat(name)
	flags := knowledgeContextWriteFlags()
	if errors.Is(err, os.ErrNotExist) {
		flags |= os.O_CREATE | os.O_EXCL
		before = nil
	} else if err != nil {
		return err
	} else {
		if manifest != nil {
			return errPathPreExists
		}
		if !before.Mode().IsRegular() {
			return errors.New("knowledge context target is unsafe")
		}
	}
	f, err := root.OpenFile(name, flags, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || (before != nil && !os.SameFile(before, opened)) {
		return errors.New("knowledge context target changed")
	}
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}
