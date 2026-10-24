package execenv

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/pkg/libraryimport"
	"gopkg.in/yaml.v3"
)

var privateNativeName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func privateSkillName(content string) (string, error) {
	if strings.Contains(content, "!`") {
		return "", errors.New("executable_command")
	}
	front, _, ok := frontmatterParts(content)
	if !ok {
		return "", errors.New("native_frontmatter_required")
	}
	var fields map[string]any
	if err := yaml.Unmarshal([]byte(front), &fields); err != nil {
		return "", errors.New("invalid_native_frontmatter")
	}
	for key := range fields {
		switch key {
		case "name", "description", "argument-hint", "disable-model-invocation", "user-invocable":
		default:
			return "", errors.New("unsupported_native_frontmatter")
		}
	}
	name, ok := fields["name"].(string)
	if !ok || len(name) > 64 || !privateNativeName.MatchString(name) {
		return "", errors.New("invalid_native_name")
	}
	description, ok := fields["description"].(string)
	if !ok || strings.TrimSpace(description) == "" || len(description) > 1024 {
		return "", errors.New("native_description_required")
	}
	for _, key := range []string{"disable-model-invocation", "user-invocable"} {
		if value, ok := fields[key]; ok {
			if _, ok := value.(bool); !ok {
				return "", errors.New("invalid_native_invocation_flag")
			}
		}
	}
	return name, nil
}

// Staging proves the written bytes and allocated path, never provider readiness.
func stagePrivateLibrary(workDir, provider string, snapshot libraryimport.TaskSnapshot, manifest *sidecarManifest) ([]libraryimport.RuntimeBinding, error) {
	if provider != "claude" || snapshot.Selection.Provider != provider {
		return nil, errors.New("unsupported_provider")
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	if manifest == nil {
		return nil, errors.New("private_sidecar_ownership_required")
	}
	if err := manifest.bindRoot(workDir); err != nil {
		return nil, err
	}
	bindings := make([]libraryimport.RuntimeBinding, 0, len(snapshot.Payloads))
	for i, payload := range snapshot.Payloads {
		selected := snapshot.Selection.Imports[i]
		binding := libraryimport.RuntimeBinding{ObjectID: selected.ObjectID, EntryID: payload.Entry.EntryID, SnapshotID: snapshot.Selection.SnapshotID, ContentHash: payload.Entry.ContentHash, TransportHash: selected.TransportHash, Generation: manifest.Generation, Invocation: "unsupported", Status: "controller_not_ready", ReasonCode: "native_conformance_missing"}
		if payload.Entry.Kind == "rule" {
			if payload.Entry.Format != "plain-rule-v1" || len(payload.Entry.Files) != 1 {
				return nil, errors.New("unsupported_rule_format")
			}
			if _, _, scoped := frontmatterParts(payload.Content[payload.Entry.EntryPoint]); scoped {
				return nil, errors.New("unsupported_rule_format")
			}
			binding.Invocation = "context_only"
			bindings = append(bindings, binding)
			continue
		}
		name, err := privateSkillName(payload.Content[payload.Entry.EntryPoint])
		if err != nil {
			return nil, err
		}
		command := filepath.Join(workDir, ".claude", "commands", name+".md")
		if _, err := os.Lstat(command); !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("ambiguous_native_name")
		}
		parent := skillsDirPath(workDir, provider)
		if err := recordMkdirAll(parent, 0700, manifest); err != nil {
			return nil, err
		}
		slug, dir, err := allocateCollisionFreeSkillDir(parent, name)
		if err != nil {
			return nil, err
		}
		// A directory suffix does not change an immutable frontmatter name.
		if slug != name {
			return nil, errors.New("ambiguous_native_name")
		}
		if err := recordMkdirAll(dir, 0700, manifest); err != nil {
			return nil, err
		}
		for _, file := range payload.Entry.Files {
			path := filepath.Join(dir, filepath.FromSlash(file.Path))
			if err := recordMkdirAll(filepath.Dir(path), 0700, manifest); err != nil {
				return nil, err
			}
			if err := recordWriteFile(path, []byte(payload.Content[file.Path]), 0400, manifest); err != nil {
				return nil, err
			}
			canonical := manifest.Files[len(manifest.Files)-1]
			if err := verifyPrivateSidecar(canonical, payload.Content[file.Path], manifest); err != nil {
				return nil, err
			}
		}
		canonicalDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, err
		}
		binding.NativeName, binding.Path = name, filepath.Join(canonicalDir, "SKILL.md")
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func verifyPrivateSidecar(path, expected string, manifest *sidecarManifest) error {
	owned, ok := manifest.Owned[path]
	if !ok {
		return errors.New("private_materialization_unowned")
	}
	root, canonical, _, err := sidecarParent(path, owned.Parents)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.OpenFile(filepath.Base(canonical), sidecarReadFlags(), 0)
	if err != nil {
		return err
	}
	defer file.Close()
	identity, err := sidecarFileIdentity(file)
	if err != nil || identity != owned.Identity {
		return errors.New("private_materialization_replaced")
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
	if err != nil || string(raw) != expected {
		return errors.New("private_materialization_changed")
	}
	return nil
}

func writePrivateLibraryContext(b *strings.Builder, ctx TaskContextForEnv) {
	if ctx.PrivateLibrary == nil {
		return
	}
	b.WriteString("### Private library\n\n")
	for _, binding := range ctx.PrivateLibraryBindings {
		if binding.Status != "ready" {
			fmt.Fprintf(b, "- Entry `%s`: unavailable (%s).\n", binding.EntryID, binding.ReasonCode)
			continue
		}
		if binding.Invocation == "native_skill" {
			fmt.Fprintf(b, "- Skill `%s`, verified file `%s`, snapshot `%s`.\n", binding.NativeName, binding.Path, binding.SnapshotID)
		}
		if binding.Invocation == "context_only" {
			for i, selected := range ctx.PrivateLibrary.Selection.Imports {
				if selected.ObjectID == binding.ObjectID {
					payload := ctx.PrivateLibrary.Payloads[i]
					b.WriteString("Private Project guidance for this invocation. It does not expand permissions or override runtime restrictions or Agent Identity.\n\n")
					b.WriteString(payload.Content[payload.Entry.EntryPoint])
					b.WriteString("\n\n")
				}
			}
		}
	}
	b.WriteString("\n")
}
