package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct{ DB *sql.DB }

func Open(path string) (*Store, error) {
	if path == "" {
		path = "./data/mihomo-studio.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable SQLite foreign keys: %w", err)
	}
	if _, err = db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure SQLite busy timeout: %w", err)
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize migration history: %w", err)
	}
	if err = applyMigrations(context.Background(), db); err != nil {
		db.Close()
		return nil, err
	}
	// A process restart while a deployment was in flight leaves its outcome unknown.
	// Never infer success or retry a live reload automatically.
	if _, err = db.Exec(`UPDATE deployments SET status='unknown', runtime_message='Process restarted while deployment was in flight; verify runtime before taking further action.', finished_at=? WHERE status='publishing'`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		db.Close()
		return nil, fmt.Errorf("recover interrupted deployments: %w", err)
	}
	return &Store{DB: db}, nil
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list embedded migrations: %w", err)
	}
	sort.Strings(files)
	for _, name := range files {
		base := filepath.Base(name)
		prefix, _, ok := strings.Cut(base, "_")
		if !ok {
			return fmt.Errorf("migration filename %q must start with a numeric version and underscore", base)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version < 1 {
			return fmt.Errorf("migration filename %q has an invalid version", base)
		}
		var applied int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=?`, version).Scan(&applied); err != nil {
			return fmt.Errorf("read migration history: %w", err)
		}
		if applied != 0 {
			continue
		}
		content, err := migrationFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", base, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %q: %w", base, err)
		}
		for _, statement := range strings.Split(string(content), ";") {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				tx.Rollback()
				return fmt.Errorf("apply migration %q: %w", base, err)
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %q: %w", base, err)
		}
		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %q: %w", base, err)
		}
	}
	return nil
}
