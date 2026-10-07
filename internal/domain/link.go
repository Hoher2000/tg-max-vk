package domain

import "time"

// MessageLink — связь между сообщением-источником и его копией в целевой платформе.
// Используется для транслирования reply/edit/delete.
type MessageLink struct {
	Source    MessageRef
	Target    MessageRef
	CreatedAt time.Time
}