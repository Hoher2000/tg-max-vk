package telegram

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/unpack"
	"github.com/gotd/td/tg"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
)

type Adapter struct {
	name        string
	apiID       int
	apiHash     string
	log         *slog.Logger
	sessionName string
	db          *sql.DB

	client *telegram.Client
	api    *tg.Client
	sender *message.Sender

	// Кэш access_hash для каналов. Ключ — ID канала.
	// Заполняется из апдейтов (e.Channels). Нужен для отправки сообщений.
	channelsMu sync.RWMutex
	channels   map[int64]int64

	mu      sync.Mutex
	running bool
}

type Config struct {
	Name        string
	APIID       int
	APIHash     string
	SessionName string
}

func New(cfg Config, db *sql.DB, log *slog.Logger) *Adapter {
	return &Adapter{
		name:        cfg.Name,
		apiID:       cfg.APIID,
		apiHash:     cfg.APIHash,
		sessionName: cfg.SessionName,
		db:          db,
		log:         log.With("adapter", cfg.Name),
		channels:    make(map[int64]int64),
	}
}

func (a *Adapter) Name() string              { return a.name }
func (a *Adapter) Platform() domain.Platform { return domain.PlatformTelegram }

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

	a.log.Info("telegram adapter starting", "session", a.sessionName)

	dispatcher := tg.NewUpdateDispatcher()
	dispatcher.OnNewMessage(a.onNewMessage(out))
	dispatcher.OnNewChannelMessage(a.onNewChannelMessage(out))

	a.client = telegram.NewClient(a.apiID, a.apiHash, telegram.Options{
		SessionStorage: newSessionStorage(a.db, a.sessionName),
		UpdateHandler:  dispatcher,
	})

	errCh := make(chan error, 1)
	readyCh := make(chan struct{})

	go func() {
		errCh <- a.client.Run(ctx, func(runCtx context.Context) error {
			a.log.Info("telegram: checking auth status")
			status, err := a.client.Auth().Status(runCtx)
			if err != nil {
				return fmt.Errorf("auth status: %w", err)
			}
			if !status.Authorized {
				return fmt.Errorf(
					"telegram session %q is not authorized; run `tg-max-vk-auth` first",
					a.sessionName,
				)
			}

			a.api = a.client.API()
			a.sender = message.NewSender(a.api)

			a.log.Info("telegram adapter ready",
				"session", a.sessionName,
				"user_id", status.User.ID,
			)

			close(readyCh)

			<-runCtx.Done()
			return nil
		})
	}()

	// Ждём либо готовности, либо ошибки, либо отмены.
	select {
	case <-ctx.Done():
		a.log.Info("telegram adapter stopping (context cancelled)")
		return nil
	case err := <-errCh:
		if err != nil {
			a.log.Error("telegram client failed", "err", err)
		}
		return err
	case <-readyCh:
		// Успешно подключились — ждём отмены ctx или ошибки.
		a.log.Info("telegram adapter running")
		select {
		case <-ctx.Done():
			a.log.Info("telegram adapter stopping")
			return nil
		case err := <-errCh:
			if err != nil {
				a.log.Error("telegram client failed", "err", err)
			}
			return err
		}
	case <-time.After(30 * time.Second):
		return fmt.Errorf(
			"telegram connection timeout after 30s; " +
				"проверь VPN/TUN/прокси (TG_PROXY) или доступность api.telegram.org",
		)
	}
}

func (a *Adapter) Stop(ctx context.Context) error {
	return nil
}

// onNewMessage возвращает обработчик апдейтов.
func (a *Adapter) onNewMessage(out chan<- *domain.Message) tg.NewMessageHandler {
	return func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		msg, ok := u.Message.(*tg.Message)
		if !ok {
			return nil
		}

		// Обновляем кэш access_hash каналов из entities.
		// Это позволяет потом резолвить каналы для отправки.
		a.channelsMu.Lock()
		for _, ch := range e.Channels {
			if !ch.Min {
				a.channels[ch.ID] = ch.AccessHash
			}
		}
		a.channelsMu.Unlock()

		chatID, err := chatIDFromPeer(msg.PeerID)
		if err != nil {
			a.log.Warn("cannot resolve peer", "err", err)
			return nil
		}

		coreMsg := &domain.Message{
			Ref: domain.MessageRef{
				Chat: chatID,
				ID:   strconv.Itoa(msg.ID),
			},
			AdapterName: a.name,
			SenderID:    senderIDFromPeer(msg.FromID),
			Text:        msg.Message,
			Timestamp:   time.Unix(int64(msg.Date), 0),
			Meta:        make(map[string]any),
		}

		// Reply header.
		if hdr, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok {
			coreMsg.ReplyTo = &domain.MessageRef{
				Chat: chatID,
				ID:   strconv.Itoa(hdr.ReplyToMsgID),
			}
		}

		a.log.Debug("personal message received",
			"chat", chatID.String(),
			"msg_id", msg.ID,
			"text", msg.Message,
		)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- coreMsg:
		}
		return nil
	}
}

// onNewChannelMessage возвращает обработчик апдейтов из каналов.
// Сообщения из каналов приходят как UpdateNewChannelMessage —
// это отдельный тип, и его надо слушать отдельно.
func (a *Adapter) onNewChannelMessage(out chan<- *domain.Message) tg.NewChannelMessageHandler {
	return func(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		msg, ok := u.Message.(*tg.Message)
		if !ok {
			return nil
		}

		// Обновляем кэш access_hash каналов.
		a.channelsMu.Lock()
		for _, ch := range e.Channels {
			if !ch.Min {
				a.channels[ch.ID] = ch.AccessHash
			}
		}
		a.channelsMu.Unlock()

		chatID, err := chatIDFromPeer(msg.PeerID)
		if err != nil {
			a.log.Warn("cannot resolve peer", "err", err)
			return nil
		}

		a.log.Debug("channel message received",
			"chat", chatID.String(),
			"msg_id", msg.ID,
			"text", msg.Message,
		)

		coreMsg := &domain.Message{
			Ref: domain.MessageRef{
				Chat: chatID,
				ID:   strconv.Itoa(msg.ID),
			},
			AdapterName: a.name,
			SenderID:    senderIDFromPeer(msg.FromID),
			Text:        msg.Message,
			Timestamp:   time.Unix(int64(msg.Date), 0),
			Meta:        make(map[string]any),
		}

		if hdr, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok {
			coreMsg.ReplyTo = &domain.MessageRef{
				Chat: chatID,
				ID:   strconv.Itoa(hdr.ReplyToMsgID),
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- coreMsg:
		}
		return nil
	}
}

// Send отправляет сообщение.
func (a *Adapter) Send(ctx context.Context, msg *domain.Message) (domain.MessageRef, error) {
	peer, err := a.resolveInputPeer(ctx, msg.Ref.Chat)
	if err != nil {
		return domain.MessageRef{}, err
	}
	upd, sendErr := a.sender.To(peer).Text(ctx, msg.Text)
	id, err := unpack.MessageID(upd, sendErr)
	if err != nil {
		return domain.MessageRef{}, fmt.Errorf("telegram send: %w", err)
	}
	return domain.MessageRef{Chat: msg.Ref.Chat, ID: strconv.Itoa(id)}, nil
}

// Reply отвечает на сообщение.
func (a *Adapter) Reply(ctx context.Context, to domain.MessageRef, msg *domain.Message) (domain.MessageRef, error) {
	peer, err := a.resolveInputPeer(ctx, msg.Ref.Chat)
	if err != nil {
		return domain.MessageRef{}, err
	}
	replyTo, err := strconv.Atoi(to.ID)
	if err != nil {
		return domain.MessageRef{}, fmt.Errorf("invalid reply target id %q: %w", to.ID, err)
	}
	upd, sendErr := a.sender.To(peer).Reply(replyTo).Text(ctx, msg.Text)
	id, err := unpack.MessageID(upd, sendErr)
	if err != nil {
		return domain.MessageRef{}, fmt.Errorf("telegram reply: %w", err)
	}
	return domain.MessageRef{Chat: msg.Ref.Chat, ID: strconv.Itoa(id)}, nil
}

// resolveInputPeer превращает domain.ChatID в tg.InputPeerClass.
func (a *Adapter) resolveInputPeer(ctx context.Context, chat domain.ChatID) (tg.InputPeerClass, error) {
	if chat.Platform() != domain.PlatformTelegram {
		return nil, fmt.Errorf("not a telegram chat: %s", chat)
	}

	rawID := chat.ID()

	switch chat.Kind() {
	case domain.ChatKindUser:
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid user id %q: %w", rawID, err)
		}
		return &tg.InputPeerUser{UserID: id}, nil

	case domain.ChatKindGroup:
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid group id %q: %w", rawID, err)
		}
		return &tg.InputPeerChat{ChatID: id}, nil

	case domain.ChatKindChannel:
		// 1. Если это юзернейм (начинается с @) — резолвим через API
		if strings.HasPrefix(rawID, "@") {
			username := strings.TrimPrefix(rawID, "@")
			req := &tg.ContactsResolveUsernameRequest{
				Username: username,
			}
			res, err := a.api.ContactsResolveUsername(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("resolve username @%s: %w", username, err)
			}

			for _, c := range res.Chats {
				if ch, ok := c.(*tg.Channel); ok {
					a.channelsMu.Lock()
					a.channels[ch.ID] = ch.AccessHash
					a.channelsMu.Unlock()
					return &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}, nil
				}
			}
			return nil, fmt.Errorf("@%s is not a channel", username)
		}

		// 2. Если это числовой ID — ищем в кэше
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid channel id %q: %w", rawID, err)
		}
		channelID, err := peerIDToChannelID(id)
		if err != nil {
			return nil, fmt.Errorf("parse channel id %q: %w", chat.ID(), err)
		}
		a.channelsMu.RLock()
		hash, ok := a.channels[channelID]
		a.channelsMu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("no access_hash for channel %d; wait for an update or use @username", channelID)
		}
		return &tg.InputPeerChannel{ChannelID: channelID, AccessHash: hash}, nil

	default:
		return nil, fmt.Errorf("unknown chat kind %q", chat.Kind())
	}
}
