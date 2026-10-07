package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/Hoher2000/tg-max-vk/internal/adapters/max"
	"github.com/Hoher2000/tg-max-vk/internal/adapters/telegram"
	"github.com/Hoher2000/tg-max-vk/internal/config"
	"github.com/Hoher2000/tg-max-vk/internal/domain"
	"github.com/Hoher2000/tg-max-vk/internal/ratelimit"
	"github.com/Hoher2000/tg-max-vk/internal/router"
	"github.com/Hoher2000/tg-max-vk/internal/storage"

	_ "modernc.org/sqlite"
)

// Run собирает всё вместе и запускает до отмены ctx.
func Run(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	// 1. Открываем БД и применяем миграции.
	db, err := openDB(ctx, cfg.Storage.Path, log)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	defer db.Close()

	store := storage.NewSQLiteStoreFromDB(db)

	// 2. Создаём адаптеры.
	adapters, err := buildAdapters(cfg, db, log)
	if err != nil {
		return fmt.Errorf("adapters: %w", err)
	}
	log.Info("adapters built", "count", len(adapters))

	// 3. Строим маршруты.
	routes, err := buildRoutes(cfg)
	if err != nil {
		return fmt.Errorf("routes: %w", err)
	}
	log.Info("routes built", "count", len(routes))

	// Создаём rate limiter.
	limiter := ratelimit.NewManager(nil, log) // nil = использовать дефолты
	defer limiter.Stop()

	// Router
	r := router.New(adapters, routes, store, limiter, log)
	if err := r.Run(ctx); err != nil {
		return fmt.Errorf("router: %w", err)
	}
	return nil
}

func openDB(ctx context.Context, path string, log *slog.Logger) (*sql.DB, error) {
	// Создаём директорию, если её нет.
	if idx := strings.LastIndexByte(path, '/'); idx > 0 {
		if err := os.MkdirAll(path[:idx], 0o755); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(ctx, p); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %q: %w", p, err)
		}
	}
	db.SetMaxOpenConns(1)

	if _, err := storage.Migrate(ctx, db, log); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

func buildAdapters(cfg *config.Config, db *sql.DB, log *slog.Logger) (map[string]domain.Adapter, error) {
	adapters := make(map[string]domain.Adapter, len(cfg.Adapters))

	for name, ac := range cfg.Adapters {
		switch ac.Type {
		case "telegram":
			a := telegram.New(telegram.Config{
				Name:        name,
				APIID:       ac.APIID,
				APIHash:     ac.APIHash,
				SessionName: ac.SessionName,
			}, db, log)
			adapters[name] = a
		case "max":
			adapters[name] = max.New(max.Config{
				Name:  name,
				Token: ac.Token,
			}, log)
		case "vk":
			return nil, fmt.Errorf("adapter %q: type %q not implemented yet", name, ac.Type)

		default:
			return nil, fmt.Errorf("adapter %q: unknown type %q", name, ac.Type)
		}
	}
	return adapters, nil
}

func buildRoutes(cfg *config.Config) ([]router.Route, error) {
	routes := make([]router.Route, 0, len(cfg.Routes))

	for _, rc := range cfg.Routes {
		fromChat, err := domain.ParseChatID(rc.FromChat)
		if err != nil {
			return nil, fmt.Errorf("route %q: from_chat: %w", rc.ID, err)
		}

		targets := make([]router.Target, 0, len(rc.Targets))
		for _, tc := range rc.Targets {
			chat, err := domain.ParseChatID(tc.Chat)
			if err != nil {
				return nil, fmt.Errorf("route %q: target chat: %w", rc.ID, err)
			}
			targets = append(targets, router.Target{
				Adapter: tc.Adapter,
				Chat:    chat,
			})
		}

		routes = append(routes, router.Route{
			ID:          rc.ID,
			FromAdapter: rc.FromAdapter,
			FromChat:    fromChat,
			Targets:     targets,
			Options: router.RouteOptions{
				ForwardAttachments: rc.Options.ForwardAttachments,
				ForwardReply:       rc.Options.ForwardReply,
				OnlyText:           rc.Options.OnlyText,
			},
		})
	}
	return routes, nil
}
