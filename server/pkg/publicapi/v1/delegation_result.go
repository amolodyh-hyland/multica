package publicapiv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxDelegationResultBytes = 32768
const HeaderResultRevision = "X-Result-Revision"

type DelegationResult struct {
	Version          int    `json:"version"`
	Format           string `json:"format"`
	DelegationID     string `json:"delegation_id"`
	TaskID           string `json:"task_id"`
	WorkspaceID      string `json:"workspace_id"`
	AgentID          string `json:"agent_id"`
	RuntimeID        string `json:"runtime_id"`
	Status           string `json:"status"`
	TerminalRevision string `json:"terminal_revision"`
	CompletedAt      string `json:"completed_at"`
	Revision         string `json:"revision"`
	Digest           string `json:"digest"`
	ByteCount        int    `json:"byte_count"`
	Text             string `json:"text"`
}

var unsafeResult = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://|www\.|(?:mul_|mat_|mcn_|mpi_|mpc_|ghp_|github_pat_|sk-)[a-z0-9_-]+|-----BEGIN|\b(?:bearer|password|passwd|secret|api[_ -]?key|access[_ -]?token)\s*[:= ]\s*\S+|(?:^|[\s\x60"'(])(?:[a-z]:[\\/]|[/\\~][\w.\\/-]+)|\b[\w.-]+[/\\][\w.\\/-]+)`)

func ValidateDelegationResultText(text string) error {
	if len(text) == 0 || len(text) > MaxDelegationResultBytes || strings.TrimSpace(text) == "" || !utf8.ValidString(text) || unsafeResult.MatchString(text) || strings.ContainsAny(text, "/\\") {
		return errors.New("result is unavailable for text export")
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return errors.New("result is unavailable for text export")
		}
	}
	return nil
}

func (r *DelegationResult) Seal() error {
	if err := ValidateDelegationResultText(r.Text); err != nil {
		return err
	}
	if r.Status != "completed" && r.Status != "failed" && r.Status != "cancelled" {
		return errors.New("result is not terminal")
	}
	if r.CompletedAt == "" {
		return errors.New("terminal snapshot missing")
	}
	r.Version, r.Format, r.ByteCount = 1, "text/plain", len(r.Text)
	sum := sha256.Sum256([]byte(r.Text))
	r.Digest = hex.EncodeToString(sum[:])
	r.Revision = ""
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	revision := sha256.Sum256(data)
	r.Revision = hex.EncodeToString(revision[:])
	return nil
}
