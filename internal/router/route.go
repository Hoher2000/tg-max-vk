package router

import "github.com/Hoher2000/tg-max-vk/internal/domain"

// Route описывает одно правило пересылки: из одного чата — в список целей.
type Route struct {
	// ID — уникальное имя маршрута. Используется в логах и для отладки.
	ID string

	// FromAdapter — имя инстанса адаптера-источника.
	FromAdapter string

	// FromChat — чат-источник.
	FromChat domain.ChatID

	// Targets — список целей, куда пересылать.
	Targets []Target

	// Options — что именно пересылать.
	Options RouteOptions
}

// Target — одна цель пересылки.
type Target struct {
	Adapter string
	Chat    domain.ChatID
}

// RouteOptions — фильтры и возможности маршрута.
type RouteOptions struct {
	// ForwardAttachments — пересылать ли вложения.
	ForwardAttachments bool

	// ForwardReply — сохранять ли reply-связь.
	ForwardReply bool

	// OnlyText — пересылать только текстовые сообщения (без вложений).
	OnlyText bool
}

// matches проверяет, подходит ли сообщение под маршрут.
func (r Route) matches(msg *domain.Message) bool {
	if msg.AdapterName != r.FromAdapter {
		return false
	}
	if !msg.Ref.Chat.Equal(r.FromChat) {
		return false
	}
	if r.Options.OnlyText && msg.HasAttachments() {
		return false
	}
	if !r.Options.ForwardAttachments && msg.HasAttachments() {
		return false
	}
	return true
}
