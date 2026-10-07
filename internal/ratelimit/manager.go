package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
)

// Manager хранит лимитеры по платформам и чатам.
// Он потокобезопасен: Wait можно вызывать из нескольких горутин.
type Manager struct {
	mu       sync.Mutex
	limits   map[domain.Platform]Limits
	globals  map[domain.Platform]*rate.Limiter
	perChat  map[chatKey]*chatLimiter
	log      *slog.Logger
	stopCh   chan struct{}
	stopOnce sync.Once
}

type chatKey struct {
	platform domain.Platform
	chatID   string
}

type chatLimiter struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// NewManager создаёт менеджер с указанными лимитами.
// Если limits == nil — используются DefaultLimits для каждой платформы.
func NewManager(limits map[domain.Platform]Limits, log *slog.Logger) *Manager {
	if limits == nil {
		limits = map[domain.Platform]Limits{}
	}
	// Заполняем дефолтами те платформы, для которых лимиты не заданы.
	for _, p := range []domain.Platform{
		domain.PlatformTelegram, domain.PlatformMax, domain.PlatformVK,
	} {
		if _, ok := limits[p]; !ok {
			limits[p] = DefaultLimits(p)
		}
	}

	m := &Manager{
		limits:  limits,
		globals: make(map[domain.Platform]*rate.Limiter),
		perChat: make(map[chatKey]*chatLimiter),
		log:     log,
		stopCh:  make(chan struct{}),
	}

	for p, l := range limits {
		m.globals[p] = rate.NewLimiter(l.Global, l.GlobalBurst)
	}

	go m.cleanupLoop()

	return m
}

// Wait блокируется до тех пор, пока можно отправлять сообщение
// в указанный чат. Учитывает и глобальный лимит, и лимит чата.
func (m *Manager) Wait(ctx context.Context, chat domain.ChatID) error {
	p := chat.Platform()

	// 1. Глобальный лимит платформы.
	global, ok := m.globals[p]
	if !ok {
		return fmt.Errorf("no rate limits configured for platform %q", p)
	}
	if err := global.Wait(ctx); err != nil {
		return fmt.Errorf("global rate limit: %w", err)
	}

	// 2. Лимит конкретного чата.
	perChat := m.getChatLimiter(p, chat.String())
	if err := perChat.Wait(ctx); err != nil {
		return fmt.Errorf("per-chat rate limit: %w", err)
	}

	return nil
}

// getChatLimiter возвращает лимитер для чата, создавая его при необходимости.
func (m *Manager) getChatLimiter(p domain.Platform, chatID string) *rate.Limiter {
	key := chatKey{platform: p, chatID: chatID}

	m.mu.Lock()
	defer m.mu.Unlock()

	cl, ok := m.perChat[key]
	if !ok {
		limits := m.limits[p]
		cl = &chatLimiter{
			limiter: rate.NewLimiter(limits.PerChat, limits.PerChatBurst),
		}
		m.perChat[key] = cl
	}
	cl.lastUsed = time.Now()
	return cl.limiter
}

// cleanupLoop периодически удаляет лимитеры чатов, которые давно не использовались.
func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.cleanup()
		}
	}
}

func (m *Manager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	removed := 0

	for key, cl := range m.perChat {
		limits := m.limits[key.platform]
		if now.Sub(cl.lastUsed) > limits.IdleTimeout {
			delete(m.perChat, key)
			removed++
		}
	}

	if removed > 0 {
		m.log.Debug("cleaned up idle rate limiters", "removed", removed)
	}
}

// Stop останавливает фоновую очистку.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
}

// Stats возвращает текущее количество активных лимитеров.
// Полезно для метрик и отладки.
func (m *Manager) Stats() (global, perChat int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.globals), len(m.perChat)
}
