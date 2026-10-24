package libraryimport

import "testing"

func TestPrivateProviderRemainsUnavailableWithoutNativeEvidence(t *testing.T) {
	for _, version := range []string{"2.1.292", "future-version"} {
		if err := RequireTaskProvider("claude", version); err == nil || err.Error() != "native_conformance_missing" {
			t.Fatalf("version label enabled native loading: %v", err)
		}
	}
	if RequireTaskProvider("claude", "") == nil || RequireTaskProvider("codex", "0.163.0") == nil {
		t.Fatal("unsupported provider enabled")
	}
}
