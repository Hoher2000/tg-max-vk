package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gotd/td/session"
)

// NewSessionStorageForAuth возвращает session.Storage поверх SQLite.
// Используется CLI-утилитой авторизации.
func NewSessionStorageForAuth(db *sql.DB, name string) session.Storage {
	return newSessionStorage(db, name)
}

// sessionStorage хранит MTProto-сессию в SQLite.
// Реализует интерфейс session.Storage из gotd/td.
type sessionStorage struct {
	db   *sql.DB
	name string
}

func newSessionStorage(db *sql.DB, name string) *sessionStorage {
	return &sessionStorage{db: db, name: name}
}

// LoadSession читает сессию. Если её нет — возвращает session.ErrNotFound,
// чтобы gotd/td понял, что нужна авторизация.
func (s *sessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM telegram_sessions WHERE name = ?`, s.name,
	).Scan(&data)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load telegram session %q: %w", s.name, err)
	}
	return data, nil
}

// StoreSession сохраняет сессию. Вызывается при каждом обновлении состояния.
func (s *sessionStorage) StoreSession(ctx context.Context, data []byte) error {
	const q = `
		INSERT INTO telegram_sessions (name, data, updated_at)
		VALUES (?, ?, unixepoch())
		ON CONFLICT(name) DO UPDATE SET
			data = excluded.data,
			updated_at = unixepoch()
	`
	if _, err := s.db.ExecContext(ctx, q, s.name, data); err != nil {
		return fmt.Errorf("store telegram session %q: %w", s.name, err)
	}
	return nil
}
