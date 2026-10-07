package router

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
	"github.com/Hoher2000/tg-max-vk/internal/ratelimit"
	"golang.org/x/time/rate"
)

// ---------- Mock Adapter ----------

type sentMessage struct {
	Chat domain.ChatID
	Text string
}

type mockAdapter struct {
	name     string
	platform domain.Platform

	mu   sync.Mutex
	sent []sentMessage

	sendErr       error
	replyErr      error
	supportsReply bool
}

func (m *mockAdapter) Name() string              { return m.name }
func (m *mockAdapter) Platform() domain.Platform { return m.platform }

func (m *mockAdapter) Start(ctx context.Context, messages chan<- *domain.Message) error {
	<-ctx.Done()
	return nil
}

func (m *mockAdapter) Stop(ctx context.Context) error { return nil }

func (m *mockAdapter) Send(ctx context.Context, msg *domain.Message) (domain.MessageRef, error) {
	if m.sendErr != nil {
		return domain.MessageRef{}, m.sendErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMessage{Chat: msg.Ref.Chat, Text: msg.Text})
	return domain.MessageRef{Chat: msg.Ref.Chat, ID: "sent-1"}, nil
}

func (m *mockAdapter) Reply(ctx context.Context, to domain.MessageRef, msg *domain.Message) (domain.MessageRef, error) {
	if m.replyErr != nil {
		return domain.MessageRef{}, m.replyErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMessage{Chat: msg.Ref.Chat, Text: msg.Text})
	return domain.MessageRef{Chat: msg.Ref.Chat, ID: "reply-1"}, nil
}

func (m *mockAdapter) SentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

// ---------- Mock Store ----------

type mockStore struct {
	mu    sync.Mutex
	links []domain.MessageLink
	err   error
}

func (m *mockStore) SaveLink(ctx context.Context, link domain.MessageLink) error {
	if m.err != nil {
		return m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.links = append(m.links, link)
	return nil
}

func (m *mockStore) LookupTargets(ctx context.Context, src domain.MessageRef) ([]domain.MessageLink, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.MessageLink
	for _, l := range m.links {
		if l.Source.Chat.Equal(src.Chat) && l.Source.ID == src.ID {
			out = append(out, l)
		}
	}
	return out, nil
}

func (m *mockStore) LookupSource(ctx context.Context, tgt domain.MessageRef) (*domain.MessageLink, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.links {
		if l.Target.Chat.Equal(tgt.Chat) && l.Target.ID == tgt.ID {
			return &l, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (m *mockStore) Close() error { return nil }

// ---------- Helpers ----------

// newTestLimiter создаёт rate limiter с очень щедрыми лимитами,
// чтобы тесты не тормозили на ожидании.
func newTestLimiter(t *testing.T) *ratelimit.Manager {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Огромные лимиты — фактически отключение rate limiting.
	limits := map[domain.Platform]ratelimit.Limits{
		domain.PlatformTelegram: {
			Global:       rate.Limit(1_000_000),
			GlobalBurst:  1_000_000,
			PerChat:      rate.Limit(1_000_000),
			PerChatBurst: 1_000_000,
			IdleTimeout:  time.Hour,
		},
		domain.PlatformMax: {
			Global:       rate.Limit(1_000_000),
			GlobalBurst:  1_000_000,
			PerChat:      rate.Limit(1_000_000),
			PerChatBurst: 1_000_000,
			IdleTimeout:  time.Hour,
		},
		domain.PlatformVK: {
			Global:       rate.Limit(1_000_000),
			GlobalBurst:  1_000_000,
			PerChat:      rate.Limit(1_000_000),
			PerChatBurst: 1_000_000,
			IdleTimeout:  time.Hour,
		},
	}

	m := ratelimit.NewManager(limits, log)
	t.Cleanup(m.Stop)
	return m
}

func newTestRouter(
	t *testing.T,
	adapters map[string]domain.Adapter,
	routes []Route,
	store domain.Store,
) *Router {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	limiter := newTestLimiter(t)
	return New(adapters, routes, store, limiter, log)
}

func mustChat(t *testing.T, p domain.Platform, k domain.ChatKind, id string) domain.ChatID {
	t.Helper()
	c, err := domain.NewChatID(p, k, id)
	if err != nil {
		t.Fatalf("NewChatID: %v", err)
	}
	return c
}

// ---------- Tests ----------

func TestHandleForwardsToAllTargets(t *testing.T) {
	tgChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-1001")
	maxChat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "42")
	vkChat := mustChat(t, domain.PlatformVK, domain.ChatKindGroup, "2001")

	tg := &mockAdapter{name: "tg", platform: domain.PlatformTelegram}
	max := &mockAdapter{name: "max", platform: domain.PlatformMax}
	vk := &mockAdapter{name: "vk", platform: domain.PlatformVK}

	store := &mockStore{}
	router := newTestRouter(t,
		map[string]domain.Adapter{"tg": tg, "max": max, "vk": vk},
		[]Route{{
			ID:          "test",
			FromAdapter: "tg",
			FromChat:    tgChat,
			Targets: []Target{
				{Adapter: "max", Chat: maxChat},
				{Adapter: "vk", Chat: vkChat},
			},
			Options: RouteOptions{ForwardAttachments: true},
		}},
		store,
	)

	msg := &domain.Message{
		Ref:         domain.MessageRef{Chat: tgChat, ID: "1"},
		AdapterName: "tg",
		Text:        "hello",
	}
	router.handle(context.Background(), msg)

	if max.SentCount() != 1 {
		t.Errorf("max sent: want 1, got %d", max.SentCount())
	}
	if vk.SentCount() != 1 {
		t.Errorf("vk sent: want 1, got %d", vk.SentCount())
	}
	if len(store.links) != 2 {
		t.Errorf("links saved: want 2, got %d", len(store.links))
	}
}

func TestHandleSkipsBridgeMessages(t *testing.T) {
	tgChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-1001")
	maxChat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "42")

	tg := &mockAdapter{name: "tg", platform: domain.PlatformTelegram}
	max := &mockAdapter{name: "max", platform: domain.PlatformMax}

	router := newTestRouter(t,
		map[string]domain.Adapter{"tg": tg, "max": max},
		[]Route{{
			ID:          "test",
			FromAdapter: "tg",
			FromChat:    tgChat,
			Targets:     []Target{{Adapter: "max", Chat: maxChat}},
		}},
		&mockStore{},
	)

	msg := &domain.Message{
		Ref:         domain.MessageRef{Chat: tgChat, ID: "1"},
		AdapterName: "tg",
		Text:        "loop",
	}
	msg.MarkFromBridge()

	router.handle(context.Background(), msg)

	if max.SentCount() != 0 {
		t.Errorf("bridge message should not be forwarded, got %d sends", max.SentCount())
	}
}

func TestHandleIgnoresNonMatchingChat(t *testing.T) {
	tgChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-1001")
	otherChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-2002")
	maxChat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "42")

	tg := &mockAdapter{name: "tg", platform: domain.PlatformTelegram}
	max := &mockAdapter{name: "max", platform: domain.PlatformMax}

	router := newTestRouter(t,
		map[string]domain.Adapter{"tg": tg, "max": max},
		[]Route{{
			ID:          "test",
			FromAdapter: "tg",
			FromChat:    tgChat,
			Targets:     []Target{{Adapter: "max", Chat: maxChat}},
		}},
		&mockStore{},
	)

	msg := &domain.Message{
		Ref:         domain.MessageRef{Chat: otherChat, ID: "1"},
		AdapterName: "tg",
		Text:        "hello",
	}
	router.handle(context.Background(), msg)

	if max.SentCount() != 0 {
		t.Errorf("non-matching chat should be ignored, got %d sends", max.SentCount())
	}
}

func TestForwardReply(t *testing.T) {
	tgChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-1001")
	maxChat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "42")

	tg := &mockAdapter{name: "tg", platform: domain.PlatformTelegram}
	max := &mockAdapter{
		name:          "max",
		platform:      domain.PlatformMax,
		supportsReply: true,
	}

	store := &mockStore{
		links: []domain.MessageLink{
			{
				Source: domain.MessageRef{Chat: tgChat, ID: "100"},
				Target: domain.MessageRef{Chat: maxChat, ID: "200"},
			},
		},
	}

	router := newTestRouter(t,
		map[string]domain.Adapter{"tg": tg, "max": max},
		[]Route{{
			ID:          "test",
			FromAdapter: "tg",
			FromChat:    tgChat,
			Targets:     []Target{{Adapter: "max", Chat: maxChat}},
			Options:     RouteOptions{ForwardReply: true},
		}},
		store,
	)

	msg := &domain.Message{
		Ref:         domain.MessageRef{Chat: tgChat, ID: "101"},
		AdapterName: "tg",
		Text:        "reply text",
		ReplyTo: &domain.MessageRef{
			Chat: tgChat,
			ID:   "100",
		},
	}
	router.handle(context.Background(), msg)

	if max.SentCount() != 1 {
		t.Fatalf("want 1 send, got %d", max.SentCount())
	}
	if max.sent[0].Text != "reply text" {
		t.Errorf("text mismatch: %q", max.sent[0].Text)
	}
}

func TestForwardReplyFallsBackWhenNoLink(t *testing.T) {
	tgChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-1001")
	maxChat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "42")

	tg := &mockAdapter{name: "tg", platform: domain.PlatformTelegram}
	max := &mockAdapter{
		name:          "max",
		platform:      domain.PlatformMax,
		supportsReply: true,
	}

	// Store пустой — нет маппинга.
	router := newTestRouter(t,
		map[string]domain.Adapter{"tg": tg, "max": max},
		[]Route{{
			ID:          "test",
			FromAdapter: "tg",
			FromChat:    tgChat,
			Targets:     []Target{{Adapter: "max", Chat: maxChat}},
			Options:     RouteOptions{ForwardReply: true},
		}},
		&mockStore{},
	)

	msg := &domain.Message{
		Ref:         domain.MessageRef{Chat: tgChat, ID: "101"},
		AdapterName: "tg",
		SenderName:  "Вася",
		Text:        "reply text",
		ReplyTo:     &domain.MessageRef{Chat: tgChat, ID: "100"},
	}
	router.handle(context.Background(), msg)

	if max.SentCount() != 1 {
		t.Fatalf("want 1 send (fallback), got %d", max.SentCount())
	}
	// Fallback должен добавить имя отправителя в текст.
	if got := max.sent[0].Text; got != "Вася:\nreply text" {
		t.Errorf("fallback text: want %q, got %q", "Вася:\nreply text", got)
	}
}

func TestOnlyTextOption(t *testing.T) {
	tgChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-1001")
	maxChat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "42")

	tg := &mockAdapter{name: "tg", platform: domain.PlatformTelegram}
	max := &mockAdapter{name: "max", platform: domain.PlatformMax}

	router := newTestRouter(t,
		map[string]domain.Adapter{"tg": tg, "max": max},
		[]Route{{
			ID:          "text-only",
			FromAdapter: "tg",
			FromChat:    tgChat,
			Targets:     []Target{{Adapter: "max", Chat: maxChat}},
			Options:     RouteOptions{OnlyText: true},
		}},
		&mockStore{},
	)

	msg := &domain.Message{
		Ref:         domain.MessageRef{Chat: tgChat, ID: "1"},
		AdapterName: "tg",
		Text:        "с картинкой",
		Attachments: []domain.Attachment{
			{Type: domain.AttachmentPhoto, URL: "https://example.com/img.jpg"},
		},
	}
	router.handle(context.Background(), msg)

	if max.SentCount() != 0 {
		t.Errorf("photo should be filtered by OnlyText, got %d sends", max.SentCount())
	}
}

func TestSendErrorDoesNotCrashRouter(t *testing.T) {
	tgChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-1001")
	maxChat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "42")

	tg := &mockAdapter{name: "tg", platform: domain.PlatformTelegram}
	max := &mockAdapter{
		name:     "max",
		platform: domain.PlatformMax,
		sendErr:  errors.New("network down"),
	}

	router := newTestRouter(t,
		map[string]domain.Adapter{"tg": tg, "max": max},
		[]Route{{
			ID:          "test",
			FromAdapter: "tg",
			FromChat:    tgChat,
			Targets:     []Target{{Adapter: "max", Chat: maxChat}},
		}},
		&mockStore{},
	)

	msg := &domain.Message{
		Ref:         domain.MessageRef{Chat: tgChat, ID: "1"},
		AdapterName: "tg",
		Text:        "hello",
	}

	// Не должно паниковать.
	router.handle(context.Background(), msg)

	if max.SentCount() != 0 {
		t.Errorf("failed send should not be recorded")
	}
}
