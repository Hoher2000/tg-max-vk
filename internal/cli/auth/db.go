package auth

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/Hoher2000/tg-max-vk/internal/storage"

	_ "modernc.org/sqlite"
)

func openAndMigrate(ctx context.Context, dbPath string, log *slog.Logger) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("pragma: %w", err)
	}
	if _, err := storage.Migrate(ctx, db, log); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}
