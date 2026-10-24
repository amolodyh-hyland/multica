package libraryimport

import "encoding/json"

func ProjectContextDigest(projectID, title, description, instructions, parentDigest string, resources any) (string, error) {
	raw, err := json.Marshal(resources)
	if err != nil {
		return "", err
	}
	var normalized []json.RawMessage
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return "", err
	}
	if normalized == nil {
		normalized = []json.RawMessage{}
	}
	return RecordDigest("private-project-context-v1", struct {
		ProjectID       string            `json:"projectId"`
		Title           string            `json:"title"`
		Description     string            `json:"description"`
		Instructions    string            `json:"instructions"`
		KnowledgeDigest string            `json:"knowledgeDigest"`
		Resources       []json.RawMessage `json:"resources"`
	}{projectID, title, description, instructions, parentDigest, normalized})
}
