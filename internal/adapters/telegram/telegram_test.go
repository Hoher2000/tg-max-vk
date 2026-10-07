package telegram

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/gotd/td/session"
	"github.com/gotd/td/tg"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
	"github.com/Hoher2000/tg-max-vk/internal/storage"

	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		t.Fatalf("pragma: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := storage.Migrate(context.Background(), db, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSessionStorage_RoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := newSessionStorage(db, "test")

	ctx := context.Background()

	// Изначально сессии нет.
	_, err := s.LoadSession(ctx)
	if !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// Сохраняем.
	want := []byte{0x01, 0x02, 0x03}
	if err := s.StoreSession(ctx, want); err != nil {
		t.Fatalf("StoreSession: %v", err)
	}

	// Читаем обратно.
	got, err := s.LoadSession(ctx)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("data mismatch: want %v, got %v", want, got)
	}

	// Перезаписываем.
	want2 := []byte{0x04, 0x05}
	if err := s.StoreSession(ctx, want2); err != nil {
		t.Fatalf("StoreSession overwrite: %v", err)
	}
	got2, _ := s.LoadSession(ctx)
	if string(got2) != string(want2) {
		t.Errorf("overwrite mismatch: want %v, got %v", want2, got2)
	}
}

func TestChatIDFromPeer(t *testing.T) {
	tests := []struct {
		name    string
		peer    tg.PeerClass
		want    string
		wantErr bool
	}{
		{
			name: "user",
			peer: &tg.PeerUser{UserID: 123},
			want: "telegram:user:123",
		},
		{
			name: "group",
			peer: &tg.PeerChat{ChatID: 456},
			want: "telegram:group:456",
		},
		{
			name: "channel",
			peer: &tg.PeerChannel{ChannelID: -1001234567890},
			want: "telegram:channel:-1001234567890",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chatIDFromPeer(tt.peer)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err: want %v, got %v", tt.wantErr, err)
			}
			if got.String() != tt.want {
				t.Errorf("want %q, got %q", tt.want, got.String())
			}
		})
	}
}

func TestChatIDFromPeer_Unknown(t *testing.T) {
	_, err := chatIDFromPeer(nil)
	if err == nil {
		t.Fatal("want error for nil peer")
	}

	_ = domain.PlatformTelegram // keep import
}
