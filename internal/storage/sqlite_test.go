package storage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLite(dbPath)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := Migrate(context.Background(), s.DB(), log); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustChat(t *testing.T, platform domain.Platform, kind domain.ChatKind, id string) domain.ChatID {
	t.Helper()
	c, err := domain.NewChatID(platform, kind, id)
	if err != nil {
		t.Fatalf("NewChatID: %v", err)
	}
	return c
}

func TestSaveAndLookupTargets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	srcChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-1001")
	dstChat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "42")

	src := domain.MessageRef{Chat: srcChat, ID: "100"}
	dst := domain.MessageRef{Chat: dstChat, ID: "200"}

	if err := s.SaveLink(ctx, domain.MessageLink{Source: src, Target: dst}); err != nil {
		t.Fatalf("SaveLink: %v", err)
	}

	links, err := s.LookupTargets(ctx, src)
	if err != nil {
		t.Fatalf("LookupTargets: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}
	if links[0].Target.ID != "200" {
		t.Errorf("target id: want 200, got %s", links[0].Target.ID)
	}
	if !links[0].Target.Chat.Equal(dstChat) {
		t.Errorf("target chat mismatch: want %s, got %s", dstChat, links[0].Target.Chat)
	}
}

func TestLookupSource(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	srcChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-1001")
	dstChat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "42")

	src := domain.MessageRef{Chat: srcChat, ID: "100"}
	dst := domain.MessageRef{Chat: dstChat, ID: "200"}

	if err := s.SaveLink(ctx, domain.MessageLink{Source: src, Target: dst}); err != nil {
		t.Fatalf("SaveLink: %v", err)
	}

	link, err := s.LookupSource(ctx, dst)
	if err != nil {
		t.Fatalf("LookupSource: %v", err)
	}
	if link.Source.ID != "100" {
		t.Errorf("source id: want 100, got %s", link.Source.ID)
	}
	if !link.Source.Chat.Equal(srcChat) {
		t.Errorf("source chat mismatch")
	}
}

func TestLookupSourceNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	chat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-1001")
	ref := domain.MessageRef{Chat: chat, ID: "999"}

	_, err := s.LookupSource(ctx, ref)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSaveLinkIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	srcChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-1001")
	dstChat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "42")

	src := domain.MessageRef{Chat: srcChat, ID: "100"}
	dst1 := domain.MessageRef{Chat: dstChat, ID: "200"}
	dst2 := domain.MessageRef{Chat: dstChat, ID: "300"}

	if err := s.SaveLink(ctx, domain.MessageLink{Source: src, Target: dst1}); err != nil {
		t.Fatalf("first SaveLink: %v", err)
	}
	// Повторная запись с тем же source+target chat, но другим message id —
	// должна обновить, а не упасть на UNIQUE.
	if err := s.SaveLink(ctx, domain.MessageLink{Source: src, Target: dst2}); err != nil {
		t.Fatalf("second SaveLink: %v", err)
	}

	links, err := s.LookupTargets(ctx, src)
	if err != nil {
		t.Fatalf("LookupTargets: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 link after upsert, got %d", len(links))
	}
	if links[0].Target.ID != "300" {
		t.Errorf("target id after upsert: want 300, got %s", links[0].Target.ID)
	}
}

func TestMultipleTargets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	srcChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-1001")
	maxChat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "42")
	vkChat := mustChat(t, domain.PlatformVK, domain.ChatKindGroup, "2000000001")

	src := domain.MessageRef{Chat: srcChat, ID: "100"}

	if err := s.SaveLink(ctx, domain.MessageLink{
		Source: src,
		Target: domain.MessageRef{Chat: maxChat, ID: "200"},
	}); err != nil {
		t.Fatalf("SaveLink max: %v", err)
	}
	if err := s.SaveLink(ctx, domain.MessageLink{
		Source: src,
		Target: domain.MessageRef{Chat: vkChat, ID: "300"},
	}); err != nil {
		t.Fatalf("SaveLink vk: %v", err)
	}

	links, err := s.LookupTargets(ctx, src)
	if err != nil {
		t.Fatalf("LookupTargets: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
}
