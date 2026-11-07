package router

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
	"github.com/Hoher2000/tg-max-vk/internal/ratelimit"
	"github.com/Hoher2000/tg-max-vk/internal/storage"

	_ "modernc.org/sqlite"
)

// ==========================================
// 1. МОК-АДАПТЕР
// ==========================================
type MockAdapter struct {
	name     string
	platform domain.Platform
	mu       sync.Mutex
	messages map[string]*domain.Message
	nextID   int
}

func NewMockAdapter(name string, platform domain.Platform) *MockAdapter {
	return &MockAdapter{
		name:     name,
		platform: platform,
		messages: make(map[string]*domain.Message),
		nextID:   1,
	}
}

func (m *MockAdapter) Name() string                                                { return m.name }
func (m *MockAdapter) Platform() domain.Platform                                   { return m.platform }
func (m *MockAdapter) Start(ctx context.Context, out chan<- *domain.Message) error { return nil }
func (m *MockAdapter) Stop(ctx context.Context) error                              { return nil }

func (m *MockAdapter) Send(ctx context.Context, msg *domain.Message) (domain.MessageRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := fmt.Sprintf("%s_%d", m.name, m.nextID)
	m.nextID++

	m.messages[id] = &domain.Message{
		Text:        msg.Text,
		Attachments: msg.Attachments,
	}
	return domain.MessageRef{Chat: msg.Ref.Chat, ID: id}, nil
}

func (m *MockAdapter) Edit(ctx context.Context, ref domain.MessageRef, newText string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg, ok := m.messages[ref.ID]; ok {
		msg.Text = newText
		return nil
	}
	return fmt.Errorf("edit failed: message %s not found", ref.ID)
}

func (m *MockAdapter) EditMedia(ctx context.Context, ref domain.MessageRef, msg *domain.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.messages[ref.ID]; ok {
		existing.Text = msg.Text
		existing.Attachments = msg.Attachments
		return nil
	}
	return fmt.Errorf("edit media failed: message %s not found", ref.ID)
}

func (m *MockAdapter) Delete(ctx context.Context, ref domain.MessageRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.messages, ref.ID)
	return nil
}

// ==========================================
// 2. ТЕСТ
// ==========================================
func TestTwoWayBridge_Integration(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// --- 1. Инициализация БД (in-memory) ---
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.NewSQLite(dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if _, err := storage.Migrate(ctx, db.DB(), log); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	store := storage.NewSQLiteStoreFromDB(db.DB())
	defer store.Close()

	// --- 2. Rate Limiter (щедрый, чтобы не тормозить тесты) ---
	limiter := ratelimit.NewManager(map[domain.Platform]ratelimit.Limits{
		domain.PlatformTelegram: {Global: 1000, GlobalBurst: 1000, PerChat: 1000, PerChatBurst: 1000, IdleTimeout: time.Minute},
		domain.PlatformMax:      {Global: 1000, GlobalBurst: 1000, PerChat: 1000, PerChatBurst: 1000, IdleTimeout: time.Minute},
	}, log)
	defer limiter.Stop()

	// --- 3. Мок-адаптеры ---
	tgMock := NewMockAdapter("telegram_main", domain.PlatformTelegram)
	maxMock := NewMockAdapter("max_main", domain.PlatformMax)

	adapters := map[string]domain.Adapter{
		"telegram_main": tgMock,
		"max_main":      maxMock,
	}

	// --- 4. Маршруты (два направления) ---
	maxChat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "-79249798")
	tgChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-10012678")

	routes := []Route{
		{
			ID:          "max-to-tg",
			FromAdapter: "max_main",
			FromChat:    maxChat,
			Targets:     []Target{{Adapter: "telegram_main", Chat: tgChat}},
			Options:     RouteOptions{ForwardAttachments: true}, // <--- Разрешаем вложения
		},
		{
			ID:          "tg-to-max",
			FromAdapter: "telegram_main",
			FromChat:    tgChat,
			Targets:     []Target{{Adapter: "max_main", Chat: maxChat}},
			Options:     RouteOptions{ForwardAttachments: true}, // <--- Разрешаем вложения
		},
	}

	mediaMock := &MockMediaStore{}
	r := New(adapters, routes, store, mediaMock, limiter, log)

	// Инициализируем "исходное" сообщение в MAX
	maxMock.messages["max-msg-1"] = &domain.Message{Text: "Привет из MAX"}

	// ==========================================
	// СЦЕНАРИЙ 1: Отправка сообщения из MAX
	// ==========================================
	msgMax := &domain.Message{
		Ref:         domain.MessageRef{Chat: maxChat, ID: "max-msg-1"},
		Action:      domain.ActionCreate,
		AdapterName: "max_main",
		Text:        "Привет из MAX",
	}
	r.handle(ctx, msgMax)

	if len(tgMock.messages) != 1 {
		t.Fatalf("TG should have 1 message, got %d", len(tgMock.messages))
	}
	var tgMsgID string
	for id := range tgMock.messages {
		tgMsgID = id
	}

	// ==========================================
	// СЦЕНАРИЙ 2: Редактирование текста в TG
	// ==========================================
	msgTgEdit := &domain.Message{
		Ref:         domain.MessageRef{Chat: tgChat, ID: tgMsgID},
		Action:      domain.ActionEdit,
		AdapterName: "telegram_main",
		Text:        "Привет, отредактированный в TG",
	}
	r.handle(ctx, msgTgEdit)

	if maxMock.messages["max-msg-1"].Text != "Привет, отредактированный в TG" {
		t.Errorf("MAX text not updated, got %q", maxMock.messages["max-msg-1"].Text)
	}

	// ==========================================
	// СЦЕНАРИЙ 3: Добавление медиа в MAX
	// ==========================================
	msgMaxMedia := &domain.Message{
		Ref:         domain.MessageRef{Chat: maxChat, ID: "max-msg-1"},
		Action:      domain.ActionEdit,
		AdapterName: "max_main",
		Text:        "Текст с картинкой",
		Attachments: []domain.Attachment{
			{Type: domain.AttachmentPhoto, MediaKey: "mock-key-1"},
		},
	}
	r.handle(ctx, msgMaxMedia)

	if len(tgMock.messages[tgMsgID].Attachments) != 1 {
		t.Errorf("TG should have 1 attachment, got %d", len(tgMock.messages[tgMsgID].Attachments))
	}

	// ==========================================
	// СЦЕНАРИЙ 4: Удаление сообщения в TG
	// ==========================================
	msgTgDelete := &domain.Message{
		Ref:         domain.MessageRef{Chat: tgChat, ID: tgMsgID},
		Action:      domain.ActionDelete,
		AdapterName: "telegram_main",
	}
	r.handle(ctx, msgTgDelete)

	if _, ok := maxMock.messages["max-msg-1"]; ok {
		t.Error("message in MAX should be deleted")
	}
}

func TestMediaAddRemoveAdd(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.NewSQLite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := storage.Migrate(ctx, db.DB(), log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store := storage.NewSQLiteStoreFromDB(db.DB())
	defer store.Close()

	limiter := ratelimit.NewManager(map[domain.Platform]ratelimit.Limits{
		domain.PlatformTelegram: {Global: 1000, GlobalBurst: 1000, PerChat: 1000, PerChatBurst: 1000, IdleTimeout: time.Minute},
		domain.PlatformMax:      {Global: 1000, GlobalBurst: 1000, PerChat: 1000, PerChatBurst: 1000, IdleTimeout: time.Minute},
	}, log)
	defer limiter.Stop()

	tgMock := NewMockAdapter("telegram_main", domain.PlatformTelegram)
	maxMock := NewMockAdapter("max_main", domain.PlatformMax)

	adapters := map[string]domain.Adapter{
		"telegram_main": tgMock,
		"max_main":      maxMock,
	}

	maxChat := mustChat(t, domain.PlatformMax, domain.ChatKindChannel, "-100")
	tgChat := mustChat(t, domain.PlatformTelegram, domain.ChatKindChannel, "-200")

	routes := []Route{
		{ID: "m2t", FromAdapter: "max_main", FromChat: maxChat, Targets: []Target{{Adapter: "telegram_main", Chat: tgChat}}, Options: RouteOptions{ForwardAttachments: true}},
	}

	r := New(adapters, routes, store, &MockMediaStore{}, limiter, log)
	maxMock.messages["max.1"] = &domain.Message{Text: "Привет"}

	// Шаг 1: создать с медиа
	r.handle(ctx, &domain.Message{
		Ref:         domain.MessageRef{Chat: maxChat, ID: "max.1"},
		Action:      domain.ActionCreate,
		AdapterName: "max_main",
		Text:        "Привет",
		Attachments: []domain.Attachment{{Type: domain.AttachmentPhoto, MediaKey: "photo-1"}},
	})
	links, _ := store.LookupTargets(ctx, domain.MessageRef{Chat: maxChat, ID: "max.1"})
	if len(links) != 1 {
		t.Fatalf("after create: expected 1 link, got %d", len(links))
	}
	if !links[0].HasMedia {
		t.Fatalf("after create: HasMedia should be true")
	}
	oldTGID := links[0].Target.ID

	// Шаг 2: удалить медиа
	r.handle(ctx, &domain.Message{
		Ref:          domain.MessageRef{Chat: maxChat, ID: "max.1"},
		Action:       domain.ActionEdit,
		AdapterName:  "max_main",
		Text:         "Привет",
		MediaRemoved: true,
	})
	links, _ = store.LookupTargets(ctx, domain.MessageRef{Chat: maxChat, ID: "max.1"})
	if len(links) != 1 {
		t.Fatalf("after remove: expected 1 link, got %d", len(links))
	}
	if links[0].HasMedia {
		t.Fatalf("after remove: HasMedia should be false")
	}
	if links[0].Target.ID == oldTGID {
		t.Fatalf("after remove: TG message should be recreated with new ID, old=%s new=%s", oldTGID, links[0].Target.ID)
	}
	newTGID := links[0].Target.ID

	// Шаг 3: снова добавить медиа
	r.handle(ctx, &domain.Message{
		Ref:         domain.MessageRef{Chat: maxChat, ID: "max.1"},
		Action:      domain.ActionEdit,
		AdapterName: "max_main",
		Text:        "Привет снова",
		Attachments: []domain.Attachment{{Type: domain.AttachmentPhoto, MediaKey: "photo-2"}},
	})
	links, _ = store.LookupTargets(ctx, domain.MessageRef{Chat: maxChat, ID: "max.1"})
	if len(links) != 1 {
		t.Fatalf("after re-add: expected 1 link, got %d", len(links))
	}
	if !links[0].HasMedia {
		t.Fatalf("after re-add: HasMedia should be true")
	}
	if links[0].Target.ID != newTGID {
		t.Errorf("after re-add: TG message ID should stay %s, got %s", newTGID, links[0].Target.ID)
	}
}

// ==========================================
// 3. ВСПОМОГАТЕЛЬНЫЕ СТРУКТУРЫ
// ==========================================
type MockMediaStore struct{}

func (m *MockMediaStore) Fetch(ctx context.Context, att domain.Attachment) (string, error) {
	return "mock-fetched-key", nil
}
func (m *MockMediaStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}
func (m *MockMediaStore) Remove(ctx context.Context, key string) error          { return nil }
func (m *MockMediaStore) GC(ctx context.Context, olderThan time.Duration) error { return nil }
func (m *MockMediaStore) Save(ctx context.Context, r io.Reader, filename string) (string, error) {
	return "mock-saved-key", nil
}
