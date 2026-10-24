package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/multica-ai/multica/server/pkg/libraryimport"
)

const maxPrivateObjectBytes = 3 * libraryimport.MaxRequestBytes

func (c *Client) privateLibraryObject(ctx context.Context, task Task, item libraryimport.SelectedImport, cached bool) (libraryimport.Object, error) {
	var object libraryimport.Object
	token, err := taskScopedAuthToken(task)
	if err != nil {
		return object, err
	}
	method := http.MethodGet
	if cached {
		method = http.MethodHead
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/api/private-library/tasks/"+url.PathEscape(task.ID)+"/imports/"+url.PathEscape(item.ObjectID), nil)
	if err != nil {
		return object, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Workspace-ID", task.WorkspaceID)
	c.setIdentityHeaders(request)
	client := *c.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return object, errors.New("private_import_unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Private-Import-Preview-Digest") != item.PreviewDigest || response.Header.Get("X-Private-Import-Content-Hash") != item.Entry.ContentHash || response.Header.Get("X-Private-Import-Transport-Hash") != item.TransportHash {
		return object, errors.New("private_import_access_or_identity_unavailable")
	}
	if cached {
		return object, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxPrivateObjectBytes+1))
	if err != nil || len(raw) > maxPrivateObjectBytes {
		return object, errors.New("private_import_response_limit")
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return object, errors.New("private_import_invalid_response")
	}
	return object, nil
}

func privateLibraryPayload(item libraryimport.SelectedImport, bundle SkillData) (libraryimport.Payload, error) {
	ref := SkillRefData{ID: item.ObjectID, Source: libraryimport.SourcePrivate, Hash: item.TransportHash, FileCount: len(item.Entry.Files) - 1}
	if !validateSkillBundle(ref, bundle) {
		return libraryimport.Payload{}, errors.New("private_transport_hash_mismatch")
	}
	payload := libraryimport.Payload{Entry: item.Entry, Content: map[string]string{item.Entry.EntryPoint: bundle.Content}}
	for _, file := range bundle.Files {
		payload.Content[file.Path] = file.Content
	}
	return payload, payload.Verify()
}

func resolvePrivateLibrary(ctx context.Context, client *Client, cache *SkillBundleCache, task Task, provider, providerVersion string) ([]libraryimport.Payload, error) {
	selection := task.PrivateLibraryManifest
	if selection == nil {
		return nil, nil
	}
	if cache == nil || !cache.private {
		return nil, errors.New("private_cache_unavailable")
	}
	root, err := cache.privateRoot(true)
	if err != nil {
		return nil, errors.New("private_cache_unavailable")
	}
	root.Close()

	if selection.Validate() != nil || selection.Provider != provider || selection.TaskID != task.ID || selection.Destination.WorkspaceID != task.WorkspaceID || selection.Binding.ProjectID != task.ProjectID || selection.Binding.RuntimeID != task.RuntimeID || selection.PersonaID != task.AgentID || task.KnowledgeManifest == nil || task.KnowledgeManifest.Validate() != nil || task.KnowledgeManifest.Digest != selection.ContextDigest || task.KnowledgeManifest.OwnerID != selection.Destination.OwnerID || strings.TrimRight(client.baseURL, "/") != selection.Destination.Origin {
		return nil, errors.New("private_selection_identity_mismatch")
	}
	namespace, err := libraryimport.RecordDigest("private-library-cache-v1", struct{ Selection, ProviderVersion string }{selection.Digest, providerVersion})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := make([]libraryimport.Payload, 0, len(selection.Imports))
	for _, item := range selection.Imports {
		ref := SkillRefData{ID: item.ObjectID, Source: libraryimport.SourcePrivate, Hash: item.TransportHash, FileCount: len(item.Entry.Files) - 1}
		var payload libraryimport.Payload
		err := cache.WithRefLock(namespace, ref, func() error {
			bundle, hit := cache.Load(namespace, ref)
			if hit {
				if _, err := client.privateLibraryObject(ctx, task, item, true); err != nil {
					return err
				}
			} else {
				object, err := client.privateLibraryObject(ctx, task, item, false)
				if err != nil {
					return err
				}
				if err := object.Verify(*selection, item); err != nil {
					return err
				}
				raw, err := json.Marshal(object.Bundle)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(raw, &bundle); err != nil {
					return err
				}
				if err := cache.Store(namespace, bundle); err != nil {
					return errors.New("private_cache_unavailable")
				}
			}
			var err error
			payload, err = privateLibraryPayload(item, bundle)
			return err
		})
		if err != nil {
			return nil, err
		}
		result = append(result, payload)
	}
	return result, nil
}

func privateLibraryByteLimit(task Task, local *knowledge.Snapshot, payloads []libraryimport.Payload) error {
	total := len(task.ProjectInstructions) + len(task.ProjectDescription) + len(task.WorkspaceContext)
	files := 0
	standing := len(task.ProjectInstructions) + len(task.WorkspaceContext)
	if task.Agent != nil {
		total += len(task.Agent.Instructions)
		standing += len(task.Agent.Instructions)
		for _, skill := range task.Agent.Skills {
			total += len(skill.Content)
			files++
			for _, file := range skill.Files {
				total += len(file.Content)
				files++
			}
		}
	}
	if local != nil {
		for _, entry := range local.Entries {
			for _, file := range entry.Files {
				total += file.SizeBytes
				files++
			}
		}
	}
	for _, payload := range payloads {
		if payload.Entry.Kind == "rule" {
			for _, file := range payload.Entry.Files {
				standing += int(file.SizeBytes)
			}
		}
		for _, file := range payload.Entry.Files {
			total += int(file.SizeBytes)
			files++
		}
	}
	if total > knowledge.MaxBytes || files > 1024 || standing > 64<<10 {
		return errors.New("runtime_limit")
	}
	return nil
}
