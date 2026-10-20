package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/joho/godotenv"

	"github.com/Hoher2000/tg-max-vk/internal/cli/auth"
)

func main() {
	// 1. Загружаем .env, если он есть. Не падаем, если файла нет —
	//    переменные могут быть заданы в окружении системы.
	_ = godotenv.Load(".env", ".env.local")

	// 2. Флаги по умолчанию берутся из env.
	//    Если флаг задан явно — он переопределит env.
	var (
		apiID       = flag.Int("api-id", envInt("TG_API_ID"), "Telegram API ID (env: TG_API_ID)")
		apiHash     = flag.String("api-hash", os.Getenv("TG_API_HASH"), "Telegram API Hash (env: TG_API_HASH)")
		phone       = flag.String("phone", os.Getenv("TG_PHONE"), "phone (env: TG_PHONE)")
		dbPath      = flag.String("db", envString("BRIDGE_DB", "./data/bridge.db"), "path to SQLite DB (env: BRIDGE_DB)")
		sessionName = flag.String("session", envString("TG_SESSION_NAME", "telegram_main"), "session name (env: TG_SESSION_NAME)")
		verify      = flag.Bool("verify", false, "verify existing session online (requires VPN/TUN)")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := auth.Config{
		APIID:       *apiID,
		APIHash:     *apiHash,
		Phone:       *phone,
		DBPath:      *dbPath,
		SessionName: *sessionName,
		Verify:      *verify,
	}

	if err := cfg.Validate(); err != nil {
		log.Error("invalid config", "err", err)
		fmt.Fprintln(os.Stderr, "Подсказка: заполни .env или передай флаги -api-id, -api-hash, -phone")
		os.Exit(2)
	}

	if err := auth.Run(context.Background(), cfg, auth.NewStdinPrompter(), log); err != nil {
		log.Error("auth failed", "err", err)
		os.Exit(1)
	}
	fmt.Println("Authorized successfully. Session saved to", *dbPath)
}

func envString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string) int {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	var n int
	fmt.Sscanf(v, "%d", &n)
	return n
}
