package domain

import (
	"context"
	"io"
	"time"
)

// Adapter — минимальный контракт мессенджера.
// Каждая платформа (Telegram, MAX, VK) реализует его.
type Adapter interface {

	// Name — уникальное имя инстанса адаптера в рамках одного Bridge.
	// Например: "telegram_main", "telegram_backup", "max_roblomania".
	Name() string

	// Platform возвращает идентификатор платформы.
	Platform() Platform

	// Start запускает приём сообщений. Блокирует до отмены ctx.
	// Все входящие сообщения отправляются в messages.
	Start(ctx context.Context, messages chan<- *Message) error

	// Stop мягко останавливает адаптер.
	Stop(ctx context.Context) error

	// Send отправляет сообщение и возвращает ссылку на него.
	Send(ctx context.Context, msg *Message) (MessageRef, error)
}

// Опциональные возможности адаптера.
// Router проверяет их через type assertion.

type Replier interface {
	Reply(ctx context.Context, to MessageRef, msg *Message) (MessageRef, error)
}

type Editor interface {
	Edit(ctx context.Context, ref MessageRef, newText string) error
}

type Deleter interface {
	Delete(ctx context.Context, ref MessageRef) error
}

// Store — персистентность.
type Store interface {
	// SaveLink сохраняет связь source → target.
	SaveLink(ctx context.Context, link MessageLink) error

	// LookupTargets возвращает все цели, куда было переслано сообщение.
	LookupTargets(ctx context.Context, src MessageRef) ([]MessageLink, error)

	// LookupSource возвращает источник по цели (для обратной трансляции).
	LookupSource(ctx context.Context, tgt MessageRef) (*MessageLink, error)

	// Close закрывает хранилище.
	Close() error
}

// MediaStore — промежуточное хранилище вложений.
type MediaStore interface {
	// Fetch скачивает вложение и возвращает ключ для последующего чтения.
	Fetch(ctx context.Context, att Attachment) (string, error)

	// Open открывает скачанный файл по ключу.
	Open(ctx context.Context, key string) (io.ReadCloser, error)

	// Remove удаляет файл.
	Remove(ctx context.Context, key string) error

	// GC удаляет файлы старше TTL.
	GC(ctx context.Context, olderThan time.Duration) error
}