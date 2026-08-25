package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/storage"
)

// Backup writes a consistent snapshot to cfg.Backup.Target (SPEC §14.2):
// SQLite mode: core db via VACUUM INTO (transaction-consistent), event
// segment files, TSDB blocks/WAL/series and a manifest. PostgreSQL
// mode: the relational backup is the DB operator's PITR job — the
// manifest records the schema version so restores can be validated;
// TSDB and artefacts are still copied.
func Backup(ctx context.Context, cfg config.Config, store *storage.Store, version string) (string, error) {
	stamp := time.Now().UTC().Format("20060102-150405")
	target := filepath.Join(cfg.Backup.Target, "northplane-"+stamp)
	if err := os.MkdirAll(target, 0o750); err != nil {
		return "", err
	}

	manifest := map[string]any{
		"format":    "northplane-backup/1",
		"version":   version,
		"createdAt": time.Now().UTC().Format(time.RFC3339),
		"storage":   store.Dialect().Name(),
	}

	if store.Dialect().Name() == "sqlite" {
		// consistent snapshot without stopping writers
		dbCopy := filepath.Join(target, "core.db")
		if _, err := store.DB().ExecContext(ctx,
			"VACUUM INTO "+sqliteQuote(dbCopy)); err != nil {
			return "", fmt.Errorf("vacuum into: %w", err)
		}
		// event segments (immutable except the current month — copied
		// last so worst case loses seconds of the hot segment)
		segments, _ := filepath.Glob(filepath.Join(cfg.DataDir, "events-*.db"))
		var copied []string
		for _, seg := range segments {
			dst := filepath.Join(target, filepath.Base(seg))
			if err := copyFile(seg, dst); err != nil {
				return "", err
			}
			copied = append(copied, filepath.Base(seg))
		}
		manifest["eventSegments"] = copied
	} else {
		var schemaVersion int
		_ = store.DB().QueryRowContext(ctx,
			`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&schemaVersion)
		manifest["schemaVersion"] = schemaVersion
		manifest["note"] = "relational backup is the PostgreSQL operator's job (PITR/pg_dump) — SPEC §14.2"
	}

	// NP-TSDB: blocks + aggregates are immutable; series journal + WAL
	// copied as-is (replay-safe).
	tsdbSrc := cfg.TSDBDir()
	if _, err := os.Stat(tsdbSrc); err == nil {
		if err := copyTree(tsdbSrc, filepath.Join(target, "tsdb")); err != nil {
			return "", fmt.Errorf("tsdb: %w", err)
		}
	}

	// E2E check artifacts (screenshots/traces, SPEC §8.6).
	artifactsSrc := cfg.ArtifactsDir()
	if _, err := os.Stat(artifactsSrc); err == nil {
		if err := copyTree(artifactsSrc, filepath.Join(target, "artifacts")); err != nil {
			return "", fmt.Errorf("artifacts: %w", err)
		}
	}

	raw, _ := json.MarshalIndent(manifest, "", "  ")
	manifestPath := filepath.Join(target, "manifest.json")
	if err := os.WriteFile(manifestPath, raw, 0o640); err != nil {
		return "", err
	}
	return manifestPath, nil
}

func sqliteQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "''") + "'"
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }() // read-only source
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	defer func() {
		// Surface a Close error on the destination only if nothing already
		// failed — a failed close can mean buffered data never reached disk.
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		return copyFile(path, target)
	})
}
