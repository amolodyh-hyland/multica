package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/pkg/knowledge"
)

func NewPrivateSkillBundleCache(root string) *SkillBundleCache {
	cache := NewSkillBundleCache(root)
	cache.private = true
	return cache
}

func (c *SkillBundleCache) privateRoot(create bool) (*os.Root, error) {
	if c.root == "" {
		return nil, errors.New("private cache root is required")
	}
	if create {
		if err := os.MkdirAll(c.root, 0700); err != nil {
			return nil, err
		}
	}
	before, err := os.Lstat(c.root)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe private cache root")
	}
	root, err := os.OpenRoot(c.root)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		root.Close()
		return nil, errors.New("private cache root changed")
	}
	if err := root.Chmod(".", 0700); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func (c *SkillBundleCache) privateCacheName(namespace string, ref SkillRefData) string {
	raw, _ := json.Marshal(struct{ Scope, Source, ID, Hash string }{namespace, ref.Source, ref.ID, ref.Hash})
	return strings.TrimPrefix(knowledge.Sum(raw), "sha256:") + ".json"
}

func (c *SkillBundleCache) loadPrivate(namespace string, ref SkillRefData) (SkillData, bool) {
	root, err := c.privateRoot(false)
	if err != nil {
		return SkillData{}, false
	}
	defer root.Close()
	name := c.privateCacheName(namespace, ref)
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPrivateObjectBytes {
		return SkillData{}, false
	}
	f, err := root.OpenFile(name, privateCacheReadFlags(), 0)
	if err != nil {
		return SkillData{}, false
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return SkillData{}, false
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxPrivateObjectBytes+1))
	var bundle SkillData
	if err != nil || len(raw) > maxPrivateObjectBytes || json.Unmarshal(raw, &bundle) != nil || !validateSkillBundle(ref, bundle) {
		return SkillData{}, false
	}
	return bundle, true
}

func (c *SkillBundleCache) storePrivate(namespace string, bundle SkillData) error {
	ref := SkillRefData{ID: bundle.ID, Source: bundle.Source, Hash: bundle.Hash, FileCount: len(bundle.Files)}
	if !validateSkillBundle(ref, bundle) {
		return errors.New("invalid private bundle")
	}
	root, err := c.privateRoot(true)
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	if len(data) > maxPrivateObjectBytes {
		return errors.New("private cache limit")
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	temp := ".bundle-" + hex.EncodeToString(token[:])
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Rename(temp, c.privateCacheName(namespace, ref))
}
