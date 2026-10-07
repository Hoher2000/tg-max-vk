package ratelimit

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
)

func newTestManager(t *testing.T, limits map[domain.Platform]Limits) *Manager {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := NewManager(limits, log)
	t.Cleanup(m.Stop)
	return m
}

func mustChat(t *testing.T, p domain.Platform, kind domain.ChatKind, id string) domain.ChatID {
	t.Helper()
	c, err := domain.NewChatID(p, kind, id)
	if err != nil {
		t.Fatalf("NewChatID: %v", err)
	}
	return c
}

func TestManager_Wait_AllowsWithinLimit(t *testing.T) {
	limits := map[domain.Platform]Limits{
		domain.PlatformMax: {
			Global:       rate.Limit(100),
			GlobalBurst:  10,
			PerChat:      rate.Limit(100),
			PerChatBurst: 10,
			IdleTimeout:  time.Minute,
		},
	}
	m := newTestManager(t, limits)
	chat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "-100")

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := m.Wait(ctx, chat); err != nil {
			t.Fatalf("Wait #%d: %v", i, err)
		}
	}
}

func TestManager_Wait_BlocksOnPerChatLimit(t *testing.T) {
	limits := map[domain.Platform]Limits{
		domain.PlatformMax: {
			Global:       rate.Limit(1000),
			GlobalBurst:  100,
			PerChat:      rate.Limit(2), // 2 msg/sec
			PerChatBurst: 1,             // burst 1
			IdleTimeout:  time.Minute,
		},
	}
	m := newTestManager(t, limits)
	chat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "-100")

	ctx := context.Background()

	start := time.Now()

	// Первый вызов проходит сразу (burst).
	if err := m.Wait(ctx, chat); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	// Второй ждёт ~500ms (1/2 sec).
	if err := m.Wait(ctx, chat); err != nil {
		t.Fatalf("second Wait: %v", err)
	}

	elapsed := time.Since(start)
	if elapsed < 400*time.Millisecond {
		t.Errorf("expected wait >= 400ms, got %v", elapsed)
	}
}

func TestManager_Wait_RespectsContext(t *testing.T) {
	limits := map[domain.Platform]Limits{
		domain.PlatformMax: {
			Global:       rate.Limit(1000),
			GlobalBurst:  100,
			PerChat:      rate.Limit(0.1), // 1 msg per 10 sec
			PerChatBurst: 1,
			IdleTimeout:  time.Minute,
		},
	}
	m := newTestManager(t, limits)
	chat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "-100")

	// Первый проходит.
	if err := m.Wait(context.Background(), chat); err != nil {
		t.Fatalf("first Wait: %v", err)
	}

	// Второй должен упасть по таймауту контекста.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := m.Wait(ctx, chat); err == nil {
		t.Error("expected context deadline error, got nil")
	}
}

func TestManager_SeparateChats(t *testing.T) {
	limits := map[domain.Platform]Limits{
		domain.PlatformMax: {
			Global:       rate.Limit(1000),
			GlobalBurst:  100,
			PerChat:      rate.Limit(1), // 1 msg/sec per chat
			PerChatBurst: 1,
			IdleTimeout:  time.Minute,
		},
	}
	m := newTestManager(t, limits)
	chat1 := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "-100")
	chat2 := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "-200")

	ctx := context.Background()

	// В оба чата по одному сообщению — проходят сразу, потому что лимиты разные.
	if err := m.Wait(ctx, chat1); err != nil {
		t.Fatalf("chat1: %v", err)
	}
	if err := m.Wait(ctx, chat2); err != nil {
		t.Fatalf("chat2: %v", err)
	}
}

func TestManager_ConcurrentWait(t *testing.T) {
	limits := map[domain.Platform]Limits{
		domain.PlatformMax: {
			Global:       rate.Limit(100),
			GlobalBurst:  50,
			PerChat:      rate.Limit(100),
			PerChatBurst: 50,
			IdleTimeout:  time.Minute,
		},
	}
	m := newTestManager(t, limits)
	chat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "-100")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Wait(context.Background(), chat); err != nil {
				t.Errorf("Wait: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestManager_Cleanup(t *testing.T) {
	limits := map[domain.Platform]Limits{
		domain.PlatformMax: {
			Global:       rate.Limit(100),
			GlobalBurst:  10,
			PerChat:      rate.Limit(100),
			PerChatBurst: 10,
			IdleTimeout:  10 * time.Millisecond,
		},
	}
	m := newTestManager(t, limits)
	chat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "-100")

	// Используем чат.
	if err := m.Wait(context.Background(), chat); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	_, perChatBefore := m.Stats()
	if perChatBefore != 1 {
		t.Fatalf("expected 1 chat limiter, got %d", perChatBefore)
	}

	// Ждём больше IdleTimeout.
	time.Sleep(50 * time.Millisecond)

	// Запускаем cleanup вручную (без ожидания тикера).
	m.cleanup()

	_, perChatAfter := m.Stats()
	if perChatAfter != 0 {
		t.Errorf("expected 0 chat limiters after cleanup, got %d", perChatAfter)
	}
}
