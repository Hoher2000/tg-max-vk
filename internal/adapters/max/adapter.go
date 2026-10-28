package max

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
)

// Adapter реализует domain.Adapter для MAX через официальный Bot API.
type Adapter struct {
	name   string
	token  string
	media  domain.MediaStore
	client *Client
	log    *slog.Logger

	mu      sync.Mutex
	running bool
}

// Config — параметры адаптера MAX.
type Config struct {
	// Name — уникальное имя инстанса, например "max_main".
	Name string

	// Token — токен бота, полученный у @MasterBot.
	Token string
}

func New(cfg Config, media domain.MediaStore, log *slog.Logger) *Adapter {
	return &Adapter{
		name:   cfg.Name,
		token:  cfg.Token,
		media:  media,
		client: NewClient(cfg.Token),
		log:    log.With("adapter", cfg.Name),
	}
}

func (a *Adapter) Name() string              { return a.name }
func (a *Adapter) Platform() domain.Platform { return domain.PlatformMax }

// Start запускает Long Polling и читает обновления.
func (a *Adapter) Start(ctx context.Context, out chan<- *domain.Message) error {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return fmt.Errorf("adapter %s already running", a.name)
	}
	a.running = true
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		a.running = false
		a.mu.Unlock()
	}()

	a.log.Info("max adapter starting")

	var marker int64
	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			a.log.Info("max adapter stopping")
			return nil
		default:
		}

		resp, err := a.client.getUpdates(ctx, marker, 30)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			a.log.Error("getUpdates failed", "err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			// Экспоненциальный backoff с потолком 30 секунд.
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			continue
		}

		backoff = time.Second
		if resp.Marker > 0 {
			marker = resp.Marker
		}

		for _, u := range resp.Updates {
			msg := a.convertUpdate(u)
			if msg == nil {
				continue
			}
			select {
			case <-ctx.Done():
				return nil
			case out <- msg:
			}
		}
	}
}

func (a *Adapter) Stop(ctx context.Context) error {
	return nil
}

// convertUpdate превращает Update в domain.Message.
// Возвращает nil, если событие не является новым сообщением.
func (a *Adapter) convertUpdate(u Update) *domain.Message {
	if u.UpdateType != "message_created" || u.Message == nil {
		return nil
	}

	msg := u.Message
	if msg.Body == nil {
		return nil
	}

	// Определяем чат и вид
	var chatKind domain.ChatKind
	var chatID int64

	if msg.Recipient != nil {
		if msg.Recipient.ChatID != 0 {
			chatID = msg.Recipient.ChatID
			switch msg.Recipient.ChatType {
			case "channel":
				chatKind = domain.ChatKindChannel
			case "chat":
				chatKind = domain.ChatKindGroup
			default:
				chatKind = domain.ChatKindGroup
			}
		} else if msg.Recipient.UserID != 0 {
			chatID = msg.Recipient.UserID
			chatKind = domain.ChatKindUser
		}
	}

	if chatID == 0 {
		a.log.Warn("cannot determine chat id for message", "update_type", u.UpdateType)
		return nil
	}

	chat, err := domain.NewChatID(domain.PlatformMax, chatKind, strconv.FormatInt(chatID, 10))
	if err != nil {
		a.log.Warn("invalid chat id", "err", err, "chat_id", chatID)
		return nil
	}

	// Сначала создаем само сообщение!
	out := &domain.Message{
		Ref: domain.MessageRef{
			Chat: chat,
			ID:   msg.Body.Mid,
		},
		AdapterName: a.name,
		Text:        msg.Body.Text,
		Timestamp:   time.UnixMilli(msg.Timestamp),
		Meta:        make(map[string]any),
	}

	// ТЕПЕРЬ парсим вложения и добавляем их в out.Attachments
	for _, att := range msg.Body.Attachments {
		attType := domain.AttachmentDocument
		if att.Type == "image" {
			attType = domain.AttachmentPhoto
		} else if att.Type == "video" {
			attType = domain.AttachmentVideo
		}

		// Извлекаем URL из payload
		var fileURL string
		if payloadMap, ok := att.Payload.(map[string]any); ok {
			if u, ok := payloadMap["url"].(string); ok {
				fileURL = u
			}
		}

		if fileURL != "" {
			out.Attachments = append(out.Attachments, domain.Attachment{
				Type: attType,
				URL:  fileURL,
			})
		} else {
			a.log.Debug("attachment without URL", "type", att.Type)
		}
	}

	if msg.Sender != nil {
		out.SenderID = strconv.FormatInt(msg.Sender.UserID, 10)
		out.SenderName = msg.Sender.FirstName
		if msg.Sender.LastName != "" {
			out.SenderName += " " + msg.Sender.LastName
		}
	}

	// Reply через link.
	if msg.Link != nil && msg.Link.Type == "reply" && msg.Link.Body != nil {
		out.ReplyTo = &domain.MessageRef{
			Chat: chat,
			ID:   msg.Link.Body.Mid,
		}
	}

	return out
}

// Send отправляет сообщение.
func (a *Adapter) Send(ctx context.Context, msg *domain.Message) (domain.MessageRef, error) {
	chatID, userID, err := parseTargetChat(msg.Ref.Chat)
	if err != nil {
		return domain.MessageRef{}, err
	}

	attachments := a.prepareAttachments(ctx, msg)

	sent, err := a.client.sendMessage(ctx, chatID, userID, SendMessageRequest{
		Text:        msg.Text,
		Attachments: attachments,
	})
	if err != nil {
		return domain.MessageRef{}, fmt.Errorf("max send: %w", err)
	}

	return domain.MessageRef{
		Chat: msg.Ref.Chat,
		ID:   sent.Body.Mid,
	}, nil
}

// Reply отвечает на сообщение.
func (a *Adapter) Reply(ctx context.Context, to domain.MessageRef, msg *domain.Message) (domain.MessageRef, error) {
	chatID, userID, err := parseTargetChat(msg.Ref.Chat)
	if err != nil {
		return domain.MessageRef{}, err
	}

	attachments := a.prepareAttachments(ctx, msg)

	// Формируем запрос с привязкой к сообщению (Reply)
	body := SendMessageRequest{
		Text:        msg.Text,
		Attachments: attachments,
		Link: &LinkedMessage{
			Type: "reply",
			Body: &MessageBody{Mid: to.ID},
		},
	}

	// Вызываем обычный sendMessage, так как API MAX универсально
	sent, err := a.client.sendMessage(ctx, chatID, userID, body)
	if err != nil {
		return domain.MessageRef{}, fmt.Errorf("max reply: %w", err)
	}

	return domain.MessageRef{
		Chat: msg.Ref.Chat,
		ID:   sent.Body.Mid,
	}, nil
}

// parseTargetChat разбирает domain.ChatID и возвращает chatID или userID.
func parseTargetChat(chat domain.ChatID) (chatID, userID int64, err error) {
	if chat.Platform() != domain.PlatformMax {
		return 0, 0, fmt.Errorf("not a max chat: %s", chat)
	}
	id, err := strconv.ParseInt(chat.ID(), 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid max chat id %q: %w", chat.ID(), err)
	}

	switch chat.Kind() {
	case domain.ChatKindUser:
		return 0, id, nil
	case domain.ChatKindGroup, domain.ChatKindChannel:
		return id, 0, nil
	default:
		return 0, 0, fmt.Errorf("unknown chat kind %q", chat.Kind())
	}
}

// prepareAttachments загружает файлы в MAX и возвращает готовые объекты для API
func (a *Adapter) prepareAttachments(ctx context.Context, msg *domain.Message) []Attachment {
	var attachments []Attachment
	for _, att := range msg.Attachments {
		if att.MediaKey == "" {
			continue
		}

		file, err := a.media.Open(ctx, att.MediaKey)
		if err != nil {
			a.log.Error("failed to open media", "key", att.MediaKey, "err", err)
			continue
		}

		token, err := a.client.uploadMedia(ctx, att, file)
		file.Close()

		if err != nil {
			a.log.Error("failed to upload media to max", "err", err)
			continue
		}

		attType := "image"
		if att.Type == domain.AttachmentVideo {
			attType = "video"
		} else if att.Type == domain.AttachmentDocument {
			attType = "file"
		}

		attachments = append(attachments, Attachment{
			Type:    attType,
			Payload: map[string]any{"token": token},
		})
	}
	return attachments
}
