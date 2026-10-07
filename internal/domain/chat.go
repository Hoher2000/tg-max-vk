package domain

import (
	"fmt"
	"strings"
)

// ChatKind — тип чата.
type ChatKind string

const (
	ChatKindUser    ChatKind = "user"
	ChatKindGroup   ChatKind = "group"
	ChatKindChannel ChatKind = "channel"
)

// ChatID — value object: идентификатор чата вместе с платформой.
// Никогда не создаётся напрямую — только через NewChatID или ParseChatID.
type ChatID struct {
	platform Platform
	kind     ChatKind
	id       string
}

func NewChatID(p Platform, k ChatKind, id string) (ChatID, error) {
	if !p.Known() {
		return ChatID{}, fmt.Errorf("unknown platform: %q", p)
	}
	if id == "" {
		return ChatID{}, fmt.Errorf("chat id is empty")
	}
	switch k {
	case ChatKindUser, ChatKindGroup, ChatKindChannel:
	default:
		return ChatID{}, fmt.Errorf("unknown chat kind: %q", k)
	}
	return ChatID{platform: p, kind: k, id: id}, nil
}

func (c ChatID) Platform() Platform { return c.platform }
func (c ChatID) Kind() ChatKind     { return c.kind }
func (c ChatID) ID() string         { return c.id }

// String — формат "platform:kind:id", например "telegram:channel:-1001234567890".
func (c ChatID) String() string {
	return fmt.Sprintf("%s:%s:%s", c.platform, c.kind, c.id)
}

// ParseChatID парсит строку формата "platform:kind:id".
func ParseChatID(s string) (ChatID, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 {
		return ChatID{}, fmt.Errorf("invalid chat id %q: expected format platform:kind:id", s)
	}
	return NewChatID(Platform(parts[0]), ChatKind(parts[1]), parts[2])
}

// Equal сравнивает два ChatID.
func (c ChatID) Equal(other ChatID) bool {
	return c.platform == other.platform && c.kind == other.kind && c.id == other.id
}