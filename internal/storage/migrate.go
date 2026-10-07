package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate применяет все миграции из встроенной файловой системы.
// Уже применённые миграции пропускаются.
// Возвращает количество применённых миграций.
func Migrate(ctx context.Context, db *sql.DB, log *slog.Logger) (int, error) {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at INTEGER NOT NULL DEFAULT (unixepoch())
		)
	`); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := listMigrations()
	if err != nil {
		return 0, err
	}

	applied := 0
	for _, name := range names {
		ok, err := isApplied(ctx, db, name)
		if err != nil {
			return applied, err
		}
		if ok {
			log.Debug("migration already applied", "name", name)
			continue
		}

		data, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return applied, fmt.Errorf("read %s: %w", name, err)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return applied, fmt.Errorf("begin tx: %w", err)
		}

		if _, err := tx.ExecContext(ctx, string(data)); err != nil {
			_ = tx.Rollback()
			return applied, fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (name) VALUES (?)`, name,
		); err != nil {
			_ = tx.Rollback()
			return applied, fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return applied, fmt.Errorf("commit %s: %w", name, err)
		}

		log.Info("migration applied", "name", name)
		applied++
	}
	return applied, nil
}

func listMigrations() ([]string, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".sql") {
			continue
		}
		if strings.HasSuffix(n, ".down.sql") {
			continue // down-миграции не поддерживаем — БД всегда двигается вперёд
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

func isApplied(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM schema_migrations WHERE name = ?`, name,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check %s: %w", name, err)
	}
	return true, nil
}
