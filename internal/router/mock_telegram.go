package router

import (
	"context"
	"fmt"
	"sync"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
)

type MockTelegramAdapter struct {
	mu     sync.Mutex
	sent   map[string]*domain.Message // mid -> message
	nextID int
}

func NewMockTelegramAdapter() *MockTelegramAdapter {
	return &MockTelegramAdapter{
		sent:   make(map[string]*domain.Message),
		nextID: 1,
	}
}

func (m *MockTelegramAdapter) Name() string              { return "telegram_mock" }
func (m *MockTelegramAdapter) Platform() domain.Platform { return domain.PlatformTelegram }
func (m *MockTelegramAdapter) Start(ctx context.Context, out chan<- *domain.Message) error {
	return nil
}
func (m *MockTelegramAdapter) Stop(ctx context.Context) error { return nil }

func (m *MockTelegramAdapter) Send(ctx context.Context, msg *domain.Message) (domain.MessageRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := fmt.Sprintf("tg.%d", m.nextID)
	m.nextID++
	m.sent[id] = msg
	return domain.MessageRef{Chat: msg.Ref.Chat, ID: id}, nil
}

func (m *MockTelegramAdapter) Edit(ctx context.Context, ref domain.MessageRef, newText string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg, ok := m.sent[ref.ID]; ok {
		msg.Text = newText
		return nil
	}
	return fmt.Errorf("message %s not found", ref.ID)
}

func (m *MockTelegramAdapter) Delete(ctx context.Context, ref domain.MessageRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sent, ref.ID)
	return nil
}

func (m *MockTelegramAdapter) EditMedia(ctx context.Context, ref domain.MessageRef, msg *domain.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if stored, ok := m.sent[ref.ID]; ok {
		stored.Attachments = msg.Attachments
		stored.Text = msg.Text
		return nil
	}
	return fmt.Errorf("message %s not found", ref.ID)
}
