package migrations

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestMigrationNumericPrefixesAreUnique(t *testing.T) {
	files := migrationFilesForLint(t, "*.up.sql")

	// Migrations through 128 contain historical duplicate numeric prefixes.
	// From 129 onward, keep the numeric sequence unique so release tooling and
	// operators can identify one schema change unambiguously by its number.
	const firstUniqueMigrationNumber = 129
	// These exact pairs shipped independently in the fork and upstream.
	// Full basenames are persisted identities; renumbering would replay applied DDL.
	historicalSyncPairs := map[string]string{
		"585_issue_workflow_owner":                   "585_personal_access_token_scopes",
		"586_cloud_agent_connection":                 "586_personal_access_token_scope_workspace",
		"587_cloud_agent_connection_provider_index":  "587_public_delegation",
		"588_daemon_token_owner":                     "588_public_delegation_id_index",
		"589_member_delegation_day":                  "589_public_delegation_identity_index",
		"590_member_delegation_day_unique_index":     "590_project_instructions",
		"591_member_delegation_day_primary_key":      "591_project_instructions_validate",
		"592_runtime_shared_with":                    "592_task_knowledge_manifest",
		"593_runtime_profile_add_devin":              "593_task_knowledge_session_index",
		"594_agent_task_process_cleanup_index":       "594_public_delegation_result",
		"595_issue_view_preference_sidebar":          "595_public_delegation_result_index",
		"596_issue_view_preference_sidebar_projects": "596_private_library_import",
		"597_issue_unread":                           "597_private_library_import_id_index",
		"598_issue_unread_unique_index":              "598_private_library_import_attempt_index",
		"599_issue_unread_primary_key":               "599_private_library_import_task_index",
		"600_task_private_library_manifest":          "600_wecom_install_session",
		"601_private_library_import_removal":         "601_wecom_install_session_due_idx",
		"602_issue_runtime_override":                 "602_wecom_install_session_request_idx",
		"603_issue_runtime_override_issue_index":     "603_wecom_install_session_agent_pending_idx",
		"604_issue_runtime_override_resolution":      "604_wecom_install_session_begin_rate_idx",
	}
	stemByNumber := make(map[int]string)
	for _, file := range files {
		stem, _, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		prefix, _, ok := strings.Cut(stem, "_")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(prefix)
		if err != nil || number < firstUniqueMigrationNumber {
			continue
		}
		if previous, exists := stemByNumber[number]; exists {
			if historicalSyncPairs[previous] == stem {
				stemByNumber[number] = stem
				continue
			}
			t.Errorf("migrations %s and %s share numeric prefix %s", previous, stem, prefix)
			continue
		}
		stemByNumber[number] = stem
	}
}

func TestMigrationFilesHaveMatchingDirections(t *testing.T) {
	files := migrationFilesForLint(t, "*.sql")

	directionsByStem := make(map[string]map[string]bool)
	for _, file := range files {
		stem, direction, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		if directionsByStem[stem] == nil {
			directionsByStem[stem] = make(map[string]bool)
		}
		directionsByStem[stem][direction] = true
	}

	for stem, directions := range directionsByStem {
		if !directions["up"] || !directions["down"] {
			t.Errorf("migration %s must have both .up.sql and .down.sql files", stem)
		}
	}
}

func migrationFilesForLint(t *testing.T, pattern string) []string {
	t.Helper()

	dir := realMigrationsDir(t)
	files, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no migration files matched %s in %s", pattern, dir)
	}
	sort.Strings(files)
	return files
}

func realMigrationsDir(t *testing.T) string {
	t.Helper()

	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration lint test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "migrations"))
}

func splitMigrationFilename(name string) (stem, direction string, ok bool) {
	for _, candidateDirection := range []string{"up", "down"} {
		suffix := fmt.Sprintf(".%s.sql", candidateDirection)
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix), candidateDirection, true
		}
	}
	return "", "", false
}
