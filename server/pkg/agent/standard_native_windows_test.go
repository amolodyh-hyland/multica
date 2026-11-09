package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStandardWindowsProviderFixture(t *testing.T) {
	if os.Getenv("MULTICA_TEST_NATIVE_PROVIDER") != "1" {
		return
	}
	if err := json.NewEncoder(os.Stdout).Encode(os.Args[1:]); err != nil {
		t.Fatal(err)
	}
}

func TestStandardWindowsNativeProviderCommand(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "native tools ü")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			executable := filepath.Join(dir, provider+".exe")
			in, err := os.Open(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			out, err := os.Create(executable)
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.Copy(out, in)
			closeErr := out.Close()
			if err != nil || closeErr != nil {
				t.Fatalf("copy native fixture: %v %v", err, closeErr)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			args := []string{"-test.run=^TestStandardWindowsProviderFixture$", "--", `C:\repo with spaces\ü`, `literal & | %PATH% "quoted"`}
			cmd := NewCommand(executable, nil).exec(ctx, args...)
			cmd.Env = append(os.Environ(), "MULTICA_TEST_NATIVE_PROVIDER=1")
			var stdout bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stdout
			if err := runOwned(cmd, slog.Default()); err != nil {
				t.Fatalf("native provider boundary: %v %s", err, stdout.String())
			}
			var got []string
			if err := json.NewDecoder(&stdout).Decode(&got); err != nil || !reflect.DeepEqual(got, args) {
				t.Fatalf("native argv changed: %v %v", got, err)
			}
		})
	}
}
