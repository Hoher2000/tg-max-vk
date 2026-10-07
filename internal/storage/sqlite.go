package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Hoher2000/tg-max-vk/internal/domain"

	_ "modernc.org/sqlite" // регистрирует драйвер "sqlite"
)

// SQLiteStore — реализация domain.Store поверх SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStoreFromDB оборачивает уже открытую *sql.DB.
// Используется в app, где БД открывается один раз для миграций и store.
func NewSQLiteStoreFromDB(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db}
}

// NewSQLite открывает БД по указанному пути и настраивает соединение.
// Миграции НЕ применяет — это отдельный шаг (см. Migrate).
func NewSQLite(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Настройки для одного процесса-писателя, много читателей.
	// WAL даёт возможность читать во время записи.
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("exec %q: %w", p, err)
		}
	}

	// SQLite плохо переносит параллельные соединения на запись.
	// Ограничиваем пул, чтобы избежать SQLITE_BUSY.
	db.SetMaxOpenConns(1)

	return &SQLiteStore{db: db}, nil
}

// DB возвращает underlying *sql.DB — нужно для Migrate.
// Не используйте снаружи пакета storage без крайней необходимости.
func (s *SQLiteStore) DB() *sql.DB { return s.db }

func (s *SQLiteStore) SaveLink(ctx context.Context, link domain.MessageLink) error {
	const q = `
		INSERT INTO message_links (
			source_platform, source_chat, source_message_id,
			target_platform, target_chat, target_message_id
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_platform, source_chat, source_message_id,
		            target_platform, target_chat)
		DO UPDATE SET target_message_id = excluded.target_message_id
	`
	_, err := s.db.ExecContext(ctx, q,
		string(link.Source.Chat.Platform()), link.Source.Chat.String(), link.Source.ID,
		string(link.Target.Chat.Platform()), link.Target.Chat.String(), link.Target.ID,
	)
	if err != nil {
		return fmt.Errorf("save link: %w", err)
	}
	return nil
}

func (s *SQLiteStore) LookupTargets(ctx context.Context, src domain.MessageRef) ([]domain.MessageLink, error) {
	const q = `
		SELECT target_platform, target_chat, target_message_id
		FROM message_links
		WHERE source_platform = ? AND source_chat = ? AND source_message_id = ?
	`
	rows, err := s.db.QueryContext(ctx, q,
		string(src.Chat.Platform()), src.Chat.String(), src.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("lookup targets: %w", err)
	}
	defer rows.Close()

	var links []domain.MessageLink
	for rows.Next() {
		var (
			tgtPlatform, tgtChatRaw, tgtMessageID string
		)
		if err := rows.Scan(&tgtPlatform, &tgtChatRaw, &tgtMessageID); err != nil {
			return nil, fmt.Errorf("scan target: %w", err)
		}
		tgtChat, err := domain.ParseChatID(tgtChatRaw)
		if err != nil {
			return nil, fmt.Errorf("parse target chat %q: %w", tgtChatRaw, err)
		}
		links = append(links, domain.MessageLink{
			Source: src,
			Target: domain.MessageRef{Chat: tgtChat, ID: tgtMessageID},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return links, nil
}

func (s *SQLiteStore) LookupSource(ctx context.Context, tgt domain.MessageRef) (*domain.MessageLink, error) {
	const q = `
		SELECT source_platform, source_chat, source_message_id
		FROM message_links
		WHERE target_platform = ? AND target_chat = ? AND target_message_id = ?
		LIMIT 1
	`
	var srcPlatform, srcChatRaw, srcMessageID string
	err := s.db.QueryRowContext(ctx, q,
		string(tgt.Chat.Platform()), tgt.Chat.String(), tgt.ID,
	).Scan(&srcPlatform, &srcChatRaw, &srcMessageID)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup source: %w", err)
	}

	srcChat, err := domain.ParseChatID(srcChatRaw)
	if err != nil {
		return nil, fmt.Errorf("parse source chat %q: %w", srcChatRaw, err)
	}
	return &domain.MessageLink{
		Source: domain.MessageRef{Chat: srcChat, ID: srcMessageID},
		Target: tgt,
	}, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
