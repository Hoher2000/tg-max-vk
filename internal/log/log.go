package log

import (
	"log/slog"
	"os"

	"github.com/Hoher2000/tg-max-vk/internal/config"
)

// New создаёт slog.Logger из конфигурации.
func New(cfg config.LogConfig) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.Level))

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	switch cfg.Format {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}
