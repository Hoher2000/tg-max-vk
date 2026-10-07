//go:build integration

package auth

import (
	"context"
	"os"
	"testing"
	"time"

	"log/slog"
)

// Требует переменных окружения:
//   TG_API_ID, TG_API_HASH, TG_PHONE — реальные данные с my.telegram.org
//   TG_CODE, TG_2FA_PASSWORD — код и пароль (или интерактив через stdin)
//
// Запуск: go test -tags=integration ./internal/cli/auth/
//
// НЕ ДОЛЖЕН запускаться в CI.
func TestIntegration_RealAuth(t *testing.T) {
	apiID := os.Getenv("TG_API_ID")
	if apiID == "" {
		t.Skip("TG_API_ID not set")
	}
	// ... остальная логика
	_ = time.Second
	_ = context.Background()
	_ = slog.Default()
}