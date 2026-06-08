package auth

// Crypto coverage for the secret store (SPEC §13.2): AES-256-GCM
// SecretBox seal/open round-trip + tamper/wrong-key detection, the
// master-key file loader, and the argon2id secret hash/verify helpers.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestBox(t *testing.T) *SecretBox {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	box, err := NewSecretBox(key)
	if err != nil {
		t.Fatalf("NewSecretBox: %v", err)
	}
	return box
}

func TestSecretBoxRoundTrip(t *testing.T) {
	box := newTestBox(t)

	cases := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"short", "hunter2"},
		{"unicode", "pässwörd-✓-日本語"},
		{"whitespace", "  spaces and\ttabs\n"},
		{"large", strings.Repeat("A", 64*1024)},
		{"binary-ish", string([]byte{0x00, 0x01, 0xff, 0x7f, 0x80})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob, err := box.Seal(tc.value)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			// ciphertext must not equal cleartext (except trivially when
			// the value is empty, where GCM still prepends nonce+tag).
			if tc.value != "" && bytes.Contains(blob, []byte(tc.value)) {
				t.Fatalf("sealed blob leaks plaintext")
			}
			got, err := box.Open(blob)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if got != tc.value {
				t.Fatalf("round-trip mismatch: got %q want %q", got, tc.value)
			}
		})
	}
}

func TestSecretBoxSealUsesFreshNonce(t *testing.T) {
	box := newTestBox(t)
	a, err := box.Seal("same-value")
	if err != nil {
		t.Fatal(err)
	}
	b, err := box.Seal("same-value")
	if err != nil {
		t.Fatal(err)
	}
	// A random nonce per seal means identical plaintext must produce
	// distinct ciphertext (no deterministic encryption leak).
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same value produced identical blobs (nonce reuse?)")
	}
}

func TestSecretBoxTamperDetection(t *testing.T) {
	box := newTestBox(t)
	blob, err := box.Seal("top-secret")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("flip-byte-in-ciphertext", func(t *testing.T) {
		bad := append([]byte(nil), blob...)
		bad[len(bad)-1] ^= 0x01 // mangle the auth tag / ciphertext tail
		if _, err := box.Open(bad); err == nil {
			t.Fatal("Open accepted tampered blob")
		}
	})

	t.Run("flip-byte-in-nonce", func(t *testing.T) {
		bad := append([]byte(nil), blob...)
		bad[0] ^= 0x01 // mangle the nonce prefix
		if _, err := box.Open(bad); err == nil {
			t.Fatal("Open accepted blob with corrupted nonce")
		}
	})

	t.Run("truncated-below-nonce-size", func(t *testing.T) {
		if _, err := box.Open(blob[:3]); err == nil {
			t.Fatal("Open accepted blob shorter than nonce")
		}
	})

	t.Run("empty-blob", func(t *testing.T) {
		if _, err := box.Open(nil); err == nil {
			t.Fatal("Open accepted empty blob")
		}
	})
}

func TestSecretBoxWrongKeyFails(t *testing.T) {
	box1 := newTestBox(t)
	box2 := newTestBox(t)
	blob, err := box1.Seal("secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box2.Open(blob); err == nil {
		t.Fatal("opening with a different key must fail")
	}
}

func TestNewSecretBoxKeySize(t *testing.T) {
	cases := []struct {
		name    string
		keyLen  int
		wantErr bool
	}{
		// aes.NewCipher accepts 16/24/32-byte keys, so NewSecretBox itself
		// does not enforce 256-bit — that policy lives in LoadMasterKey
		// (covered by TestLoadMasterKeyErrors/wrong-length).
		{"valid-256", 32, false},
		{"aes-192", 24, false},
		{"aes-128", 16, false},
		{"invalid-len-33", 33, true},
		{"invalid-len-20", 20, true},
		{"empty", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSecretBox(make([]byte, tc.keyLen))
			if tc.wantErr && err == nil {
				t.Fatalf("key len %d: want error, got nil", tc.keyLen)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("key len %d: unexpected error %v", tc.keyLen, err)
			}
		})
	}
}

func TestGenerateAndLoadMasterKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")

	if err := GenerateMasterKey(path); err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}

	// File permissions must be locked down (0600).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key file perms = %o, want 600", perm)
	}

	box, err := LoadMasterKey(path)
	if err != nil {
		t.Fatalf("LoadMasterKey: %v", err)
	}
	// The loaded box must be functional and round-trip.
	blob, err := box.Seal("via-loaded-key")
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Open(blob)
	if err != nil || got != "via-loaded-key" {
		t.Fatalf("loaded box round-trip: got %q err %v", got, err)
	}
}

func TestLoadMasterKeyErrors(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing-file", func(t *testing.T) {
		if _, err := LoadMasterKey(filepath.Join(dir, "nope.key")); err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("not-hex", func(t *testing.T) {
		p := filepath.Join(dir, "bad-hex.key")
		if err := os.WriteFile(p, []byte("zzzz-not-hex-zzzz"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadMasterKey(p); err == nil {
			t.Fatal("expected error for non-hex key")
		}
	})

	t.Run("wrong-length", func(t *testing.T) {
		p := filepath.Join(dir, "short.key")
		// valid hex but only 16 bytes (32 hex chars) — must be 32 bytes.
		if err := os.WriteFile(p, []byte(hex.EncodeToString(make([]byte, 16))), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadMasterKey(p); err == nil {
			t.Fatal("expected error for 16-byte key")
		}
	})

	t.Run("accepts-trailing-whitespace", func(t *testing.T) {
		p := filepath.Join(dir, "ws.key")
		// GenerateMasterKey writes a trailing newline; the loader must trim it.
		if err := os.WriteFile(p, []byte(hex.EncodeToString(make([]byte, 32))+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadMasterKey(p); err != nil {
			t.Fatalf("loader should trim whitespace: %v", err)
		}
	})
}

func TestHashAndVerifySecret(t *testing.T) {
	const secret = "korrekt-pferd-batterie-heftklammer"
	stored := HashSecret(secret)

	t.Run("format-is-salt-dollar-hash", func(t *testing.T) {
		salt, hash, ok := strings.Cut(stored, "$")
		if !ok || salt == "" || hash == "" {
			t.Fatalf("hash format unexpected: %q", stored)
		}
		// Both halves are hex-encoded.
		if _, err := hex.DecodeString(salt); err != nil {
			t.Fatalf("salt not hex: %v", err)
		}
		if _, err := hex.DecodeString(hash); err != nil {
			t.Fatalf("hash not hex: %v", err)
		}
	})

	t.Run("correct-secret-verifies", func(t *testing.T) {
		if !VerifySecret(secret, stored) {
			t.Fatal("correct secret failed to verify")
		}
	})

	t.Run("wrong-secret-rejected", func(t *testing.T) {
		if VerifySecret("wrong-password", stored) {
			t.Fatal("wrong secret verified")
		}
	})

	t.Run("salt-randomised-per-hash", func(t *testing.T) {
		other := HashSecret(secret)
		if other == stored {
			t.Fatal("two hashes of the same secret are identical (salt reuse?)")
		}
		// Yet both still verify the same input.
		if !VerifySecret(secret, other) {
			t.Fatal("second hash failed to verify")
		}
	})
}

func TestVerifySecretMalformedStored(t *testing.T) {
	cases := []struct {
		name   string
		stored string
	}{
		{"empty", ""},
		{"no-separator", "deadbeef"},
		{"salt-not-hex", "zz$" + hex.EncodeToString(make([]byte, 32))},
		{"hash-not-hex", hex.EncodeToString(make([]byte, 16)) + "$zz"},
		{"both-empty-halves", "$"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if VerifySecret("anything", tc.stored) {
				t.Fatalf("VerifySecret accepted malformed stored value %q", tc.stored)
			}
		})
	}
}
