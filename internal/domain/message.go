package domain

import "time"

// AttachmentType — тип вложения.
type AttachmentType string

const (
	AttachmentPhoto    AttachmentType = "photo"
	AttachmentVideo    AttachmentType = "video"
	AttachmentAudio    AttachmentType = "audio"
	AttachmentVoice    AttachmentType = "voice"
	AttachmentDocument AttachmentType = "document"
	AttachmentSticker  AttachmentType = "sticker"
)

// Attachment — вложение сообщения.
type Attachment struct {
	Type     AttachmentType
	MediaKey string
	URL      string // Для HTTP-скачивания (MAX)
	FileID   string // Для Telegram
	FileSize int64
	FileName string
	MimeType string
}

// MessageRef — ссылка на конкретное сообщение в конкретном чате.
type MessageRef struct {
	Chat ChatID
	ID   string
}

func (r MessageRef) IsZero() bool {
	return r.ID == "" && r.Chat.ID() == ""
}

// Message — универсальное сообщение моста.
type Message struct {
	Ref         MessageRef
	AdapterName string // имя инстанса адаптера, который доставил сообщение
	SenderID    string
	SenderName  string
	Text        string
	Attachments []Attachment
	ReplyTo     *MessageRef
	Timestamp   time.Time
	Meta        map[string]any
}

// HasAttachments возвращает true, если есть вложения.
func (m *Message) HasAttachments() bool {
	return len(m.Attachments) > 0
}

// IsFromBridge проверяет, что сообщение уже было переслано мостом.
// Защищает от эхо-циклов.
func (m *Message) IsFromBridge() bool {
	if m.Meta == nil {
		return false
	}
	v, _ := m.Meta["from_bridge"].(bool)
	return v
}

// MarkFromBridge помечает сообщение как созданное мостом.
func (m *Message) MarkFromBridge() {
	if m.Meta == nil {
		m.Meta = make(map[string]any)
	}
	m.Meta["from_bridge"] = true
}
