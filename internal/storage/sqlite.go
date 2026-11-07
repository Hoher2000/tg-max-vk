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
	if link.Source.Chat.ID() == "" || link.Target.Chat.ID() == "" {
		return fmt.Errorf("cannot save link: source or target chat ID is empty")
	}

	const q = `
		INSERT INTO message_links (
			source_platform, source_chat, source_message_id,
			target_platform, target_chat, target_message_id, has_media
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_platform, source_chat, source_message_id, target_platform, target_chat)
		DO UPDATE SET 
			target_message_id = excluded.target_message_id,
			has_media = excluded.has_media
	`
	_, err := s.db.ExecContext(ctx, q,
		string(link.Source.Chat.Platform()), link.Source.Chat.String(), link.Source.ID,
		string(link.Target.Chat.Platform()), link.Target.Chat.String(), link.Target.ID,
		link.HasMedia, // <--- ДОБАВЛЕНО ЗДЕСЬ
	)
	if err != nil {
		return fmt.Errorf("save link: %w", err)
	}
	return nil
}

func (s *SQLiteStore) LookupTargets(ctx context.Context, src domain.MessageRef) ([]domain.MessageLink, error) {
	var (
		rows *sql.Rows
		err  error
	)

	if src.Chat.ID() != "" {
		const q = `
			SELECT target_platform, target_chat, target_message_id, has_media
			FROM message_links
			WHERE source_platform = ? AND source_chat = ? AND source_message_id = ?
		`
		rows, err = s.db.QueryContext(ctx, q, string(src.Chat.Platform()), src.Chat.String(), src.ID)
	} else {
		const q = `
			SELECT target_platform, target_chat, target_message_id, has_media
			FROM message_links
			WHERE source_message_id = ?
		`
		rows, err = s.db.QueryContext(ctx, q, src.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("lookup targets: %w", err)
	}
	defer rows.Close()

	var links []domain.MessageLink
	for rows.Next() {
		var tgtPlatform, tgtChatRaw, tgtMessageID string
		var hasMedia bool
		if err := rows.Scan(&tgtPlatform, &tgtChatRaw, &tgtMessageID, &hasMedia); err != nil { // <--- ДОБАВЛЕНО
			return nil, fmt.Errorf("scan target: %w", err)
		}
		tgtChat, err := domain.ParseChatID(tgtChatRaw)
		if err != nil {
			continue
		}
		links = append(links, domain.MessageLink{
			Source:   src,
			Target:   domain.MessageRef{Chat: tgtChat, ID: tgtMessageID},
			HasMedia: hasMedia, // <--- ДОБАВЛЕНО
		})
	}
	return links, nil
}

func (s *SQLiteStore) LookupSource(ctx context.Context, tgt domain.MessageRef) (*domain.MessageLink, error) {
	const q = `
		SELECT source_platform, source_chat, source_message_id, has_media
		FROM message_links
		WHERE target_platform = ? AND target_chat = ? AND target_message_id = ?
		LIMIT 1
	`
	var srcPlatform, srcChatRaw, srcMessageID string
	var hasMedia bool
	err := s.db.QueryRowContext(ctx, q,
		string(tgt.Chat.Platform()), tgt.Chat.String(), tgt.ID,
	).Scan(&srcPlatform, &srcChatRaw, &srcMessageID, &hasMedia) // <--- ДОБАВЛЕНО

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
		Source:   domain.MessageRef{Chat: srcChat, ID: srcMessageID},
		Target:   tgt,
		HasMedia: hasMedia, // <--- ДОБАВЛЕНО
	}, nil
}

func (s *SQLiteStore) DeleteLink(ctx context.Context, link domain.MessageLink) error {
	const q = `
		DELETE FROM message_links
		WHERE source_platform = ? AND source_chat = ? AND source_message_id = ?
		  AND target_platform = ? AND target_chat = ?
	`
	_, err := s.db.ExecContext(ctx, q,
		string(link.Source.Chat.Platform()), link.Source.Chat.String(), link.Source.ID,
		string(link.Target.Chat.Platform()), link.Target.Chat.String(),
	)
	if err != nil {
		return fmt.Errorf("delete link: %w", err)
	}
	return nil
}

// MoveLink атомарно заменяет старую связь на новую в одной транзакции.
func (s *SQLiteStore) MoveLink(ctx context.Context, oldLink, newLink domain.MessageLink) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// 1. Удаляем старую связь
	_, err = tx.ExecContext(ctx, `
		DELETE FROM message_links
		WHERE source_platform = ? AND source_chat = ? AND source_message_id = ?
		  AND target_platform = ? AND target_chat = ?
	`,
		string(oldLink.Source.Chat.Platform()), oldLink.Source.Chat.String(), oldLink.Source.ID,
		string(oldLink.Target.Chat.Platform()), oldLink.Target.Chat.String(),
	)
	if err != nil {
		return fmt.Errorf("delete old link: %w", err)
	}

	// 2. Вставляем новую
	_, err = tx.ExecContext(ctx, `
		INSERT INTO message_links (
			source_platform, source_chat, source_message_id,
			target_platform, target_chat, target_message_id, has_media
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_platform, source_chat, source_message_id, target_platform, target_chat)
		DO UPDATE SET 
			target_message_id = excluded.target_message_id,
			has_media = excluded.has_media
	`,
		string(newLink.Source.Chat.Platform()), newLink.Source.Chat.String(), newLink.Source.ID,
		string(newLink.Target.Chat.Platform()), newLink.Target.Chat.String(), newLink.Target.ID,
		newLink.HasMedia,
	)
	if err != nil {
		return fmt.Errorf("insert new link: %w", err)
	}

	return tx.Commit()
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
