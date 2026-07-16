package server

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/config"
)

// resolveSecretBox self-provisions the master key so secrets-at-rest
// never silently disable: explicit path when usable (generated on first
// boot), dataDir fallback when the explicit path is broken (the classic
// docker bind-mount-to-missing-file leaves a directory behind).
func TestResolveSecretBox(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	roundtrips := func(t *testing.T, box *auth.SecretBox) {
		t.Helper()
		if box == nil {
			t.Fatal("expected a usable secret box")
		}
		sealed, err := box.Seal("s3cret")
		if err != nil {
			t.Fatal(err)
		}
		got, err := box.Open(sealed)
		if err != nil || got != "s3cret" {
			t.Fatalf("roundtrip: %q %v", got, err)
		}
	}

	t.Run("no config generates key in dataDir", func(t *testing.T) {
		dir := t.TempDir()
		box := resolveSecretBox(config.Config{DataDir: dir}, log)
		roundtrips(t, box)
		if _, err := os.Stat(filepath.Join(dir, "secret.key")); err != nil {
			t.Fatalf("key file not created: %v", err)
		}
		// Second boot loads the same key: blobs stay decryptable.
		sealed, _ := box.Seal("persist")
		again := resolveSecretBox(config.Config{DataDir: dir}, log)
		if got, err := again.Open(sealed); err != nil || got != "persist" {
			t.Fatalf("key not stable across boots: %q %v", got, err)
		}
	})

	t.Run("explicit missing file is generated there", func(t *testing.T) {
		dir := t.TempDir()
		keyPath := filepath.Join(dir, "custom.key")
		box := resolveSecretBox(config.Config{DataDir: dir, SecretKeyFile: keyPath}, log)
		roundtrips(t, box)
		if _, err := os.Stat(keyPath); err != nil {
			t.Fatalf("explicit key not created: %v", err)
		}
	})

	t.Run("explicit valid file is used", func(t *testing.T) {
		dir := t.TempDir()
		keyPath := filepath.Join(dir, "existing.key")
		if err := auth.GenerateMasterKey(keyPath); err != nil {
			t.Fatal(err)
		}
		want, err := auth.LoadMasterKey(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		sealed, _ := want.Seal("preexisting")
		box := resolveSecretBox(config.Config{DataDir: dir, SecretKeyFile: keyPath}, log)
		if got, err := box.Open(sealed); err != nil || got != "preexisting" {
			t.Fatalf("explicit key not used: %q %v", got, err)
		}
	})

	t.Run("broken explicit path falls back to dataDir", func(t *testing.T) {
		dir := t.TempDir()
		// A directory at the key path — what a docker bind mount leaves
		// behind when the host-side file is missing.
		brokenPath := filepath.Join(dir, "mounted.key")
		if err := os.Mkdir(brokenPath, 0o755); err != nil {
			t.Fatal(err)
		}
		box := resolveSecretBox(config.Config{DataDir: dir, SecretKeyFile: brokenPath}, log)
		roundtrips(t, box)
		if _, err := os.Stat(filepath.Join(dir, "secret.key")); err != nil {
			t.Fatalf("fallback key not created: %v", err)
		}
	})

	t.Run("garbage explicit file is not overwritten", func(t *testing.T) {
		dir := t.TempDir()
		keyPath := filepath.Join(dir, "garbage.key")
		if err := os.WriteFile(keyPath, []byte("not-hex"), 0o600); err != nil {
			t.Fatal(err)
		}
		box := resolveSecretBox(config.Config{DataDir: dir, SecretKeyFile: keyPath}, log)
		roundtrips(t, box) // fallback works
		raw, _ := os.ReadFile(keyPath)
		if string(raw) != "not-hex" {
			t.Fatal("operator file must not be overwritten")
		}
	})
}
