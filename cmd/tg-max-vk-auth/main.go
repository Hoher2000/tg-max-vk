package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/Hoher2000/tg-max-vk/internal/cli/auth"
)

func main() {
	var (
		apiID       = flag.Int("api-id", 0, "Telegram API ID (my.telegram.org)")
		apiHash     = flag.String("api-hash", "", "Telegram API Hash")
		phone       = flag.String("phone", "", "phone in international format, e.g. +79991234567")
		dbPath      = flag.String("db", "./data/bridge.db", "path to SQLite DB")
		sessionName = flag.String("session", "telegram_main", "session name in DB")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := auth.Config{
		APIID:       *apiID,
		APIHash:     *apiHash,
		Phone:       *phone,
		DBPath:      *dbPath,
		SessionName: *sessionName,
	}

	if err := auth.Run(context.Background(), cfg, auth.NewStdinPrompter(), log); err != nil {
		log.Error("auth failed", "err", err)
		os.Exit(1)
	}
	fmt.Println("Authorized successfully. Session saved to", dbPath)
}
