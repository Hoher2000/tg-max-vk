package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"image"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/message/unpack"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"

	_ "image/gif" // регистрируем декодер GIF
	"image/jpeg"
	_ "image/jpeg" // регистрируем декодер JPEG
	_ "image/png"  // регистрируем декодер PNG

	// Если нужно поддерживать WebP, придется добавить зависимость
	"github.com/Hoher2000/tg-max-vk/internal/domain"
	_ "golang.org/x/image/webp"
)

type Adapter struct {
	name        string
	apiID       int
	apiHash     string
	log         *slog.Logger
	sessionName string
	db          *sql.DB
	media       domain.MediaStore
	client      *telegram.Client
	api         *tg.Client
	sender      *message.Sender

	// Кэш access_hash для каналов. Ключ — ID канала.
	// Заполняется из апдейтов (e.Channels). Нужен для отправки сообщений.
	channelsMu sync.RWMutex
	channels   map[int64]int64
	ready      chan struct{}
	mu         sync.Mutex
	running    bool
}

type Config struct {
	Name        string
	APIID       int
	APIHash     string
	SessionName string
}

func New(cfg Config, db *sql.DB, media domain.MediaStore, log *slog.Logger) *Adapter {
	return &Adapter{
		name:        cfg.Name,
		apiID:       cfg.APIID,
		apiHash:     cfg.APIHash,
		sessionName: cfg.SessionName,
		media:       media,
		db:          db,
		ready:       make(chan struct{}),
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
			close(a.ready)
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

func (a *Adapter) FetchMedia(ctx context.Context, msg *domain.Message) error {
	// Если заглушки нет, значит и медиа нет
	if len(msg.Attachments) == 0 || msg.Attachments[0].Type != "pending" {
		return nil
	}

	// Вызываем нашу старую функцию скачивания
	atts, err := a.downloadMedia(ctx, msg.Ref.Chat, func() int { id, _ := strconv.Atoi(msg.Ref.ID); return id }())
	if err != nil {
		return err
	}

	// Заменяем заглушку на реальные файлы
	msg.Attachments = atts
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

		// Если есть медиа, добавляем заглушку, чтобы роутер знал о вложении
		if msg.Media != nil {
			coreMsg.Attachments = append(coreMsg.Attachments, domain.Attachment{
				Type: "pending", // Заглушка
			})
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

		// Если есть медиа, добавляем заглушку, чтобы роутер знал о вложении
		if msg.Media != nil {
			coreMsg.Attachments = append(coreMsg.Attachments, domain.Attachment{
				Type: "pending", // Заглушка
			})
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

// Отправка
func (a *Adapter) Send(ctx context.Context, msg *domain.Message) (domain.MessageRef, error) {
	peer, err := a.resolveInputPeer(ctx, msg.Ref.Chat)
	if err != nil {
		return domain.MessageRef{}, err
	}
	upd, err := a.sendWithMedia(ctx, peer, msg)
	if err != nil {
		return domain.MessageRef{}, fmt.Errorf("telegram send: %w", err)
	}
	id, err := unpack.MessageID(upd, nil)
	return domain.MessageRef{Chat: msg.Ref.Chat, ID: strconv.Itoa(id)}, nil
}

// Ответ
func (a *Adapter) Reply(ctx context.Context, to domain.MessageRef, msg *domain.Message) (domain.MessageRef, error) {
	peer, err := a.resolveInputPeer(ctx, msg.Ref.Chat)
	if err != nil {
		return domain.MessageRef{}, err
	}
	replyTo, _ := strconv.Atoi(to.ID)
	upd, err := a.sendWithMedia(ctx, peer, msg, replyTo)
	if err != nil {
		return domain.MessageRef{}, fmt.Errorf("telegram reply: %w", err)
	}
	id, err := unpack.MessageID(upd, nil)
	return domain.MessageRef{Chat: msg.Ref.Chat, ID: strconv.Itoa(id)}, nil
}

// Вспомогательный метод для загрузки медиа и отправки
func (a *Adapter) sendWithMedia(ctx context.Context, peer tg.InputPeerClass, msg *domain.Message, replyTo ...int) (tg.UpdatesClass, error) {
	req := a.sender.To(peer)

	var builder *message.Builder
	if len(replyTo) > 0 {
		builder = req.Reply(replyTo[0])
	} else {
		builder = &req.Builder
	}

	if len(msg.Attachments) == 0 {
		return builder.Text(ctx, msg.Text)
	}

	att := msg.Attachments[0]
	file, err := a.media.Open(ctx, att.MediaKey)
	if err != nil {
		return nil, fmt.Errorf("open media: %w", err)
	}
	defer file.Close()

	up := uploader.NewUploader(a.api)

	// --- ОБРАБОТКА ФОТО ---
	if att.Type == domain.AttachmentPhoto {
		// Читаем файл в память ОДИН раз
		imgData, err := io.ReadAll(file)
		if err != nil {
			return nil, fmt.Errorf("read image: %w", err)
		}
		if len(imgData) == 0 {
			return nil, fmt.Errorf("image file is empty")
		}

		// Пытаемся декодировать (WebP уже должен читаться благодаря импорту)
		img, _, err := image.Decode(bytes.NewReader(imgData))
		if err != nil {
			a.log.Warn("cannot decode image, sending as document", "err", err)
			// Если не смогли декодировать, шлем как файл, используя FromBytes
			tgFile, err := up.FromBytes(ctx, att.FileName, imgData)
			if err != nil {
				return nil, fmt.Errorf("upload raw bytes: %w", err)
			}
			doc := message.UploadedDocument(tgFile, styling.Plain(msg.Text)).
				Filename(att.FileName).
				MIME("image/webp")
			return builder.Media(ctx, doc)
		}

		// Кодируем в 100% валидный JPEG для Telegram
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
			return nil, fmt.Errorf("encode jpeg: %w", err)
		}

		tgFile, err := up.FromBytes(ctx, "photo.jpg", buf.Bytes())
		if err != nil {
			return nil, fmt.Errorf("upload jpeg: %w", err)
		}
		return builder.Media(ctx, message.UploadedPhoto(tgFile, styling.Plain(msg.Text)))
	}

	// --- ОБРАБОТКА ВИДЕО И ДОКУМЕНТОВ ---
	// Для видео и документов читаем через FromReader (файл еще не читали)
	// --- ОБРАБОТКА ВИДЕО ---
	if att.Type == domain.AttachmentVideo {
		tgFile, err := up.FromReader(ctx, att.FileName, file)
		if err != nil {
			return nil, fmt.Errorf("upload video: %w", err)
		}

		// message.Video создает VideoDocumentBuilder, а SupportsStreaming() включает стриминг
		video := message.Video(tgFile, styling.Plain(msg.Text)).SupportsStreaming()

		return builder.Media(ctx, video)
	}

	// --- ОБРАБОТКА ДОКУМЕНТОВ ---
	// Теперь filename будет нормальным (например, "document.pdf")
	filename := att.FileName
	if filename == "" || filename == "file" {
		filename = "document.bin"
	}

	tgFile, err := up.FromReader(ctx, filename, file)
	if err != nil {
		return nil, fmt.Errorf("upload to tg: %w", err)
	}

	// Определяем MIME-тип
	mimeType := att.MimeType
	if mimeType == "" || mimeType == "application/octet-stream" {
		if strings.HasSuffix(filename, ".pdf") {
			mimeType = "application/pdf"
		} else if strings.HasSuffix(filename, ".xlsx") {
			mimeType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		} else if strings.HasSuffix(filename, ".docx") {
			mimeType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		} else if strings.HasSuffix(filename, ".zip") {
			mimeType = "application/zip"
		} else {
			mimeType = "application/octet-stream"
		}
	}

	doc := message.UploadedDocument(tgFile, styling.Plain(msg.Text)).
		Filename(filename).
		ForceFile(true).
		MIME(mimeType)

	return builder.Media(ctx, doc)
}

// resolveInputPeer превращает domain.ChatID в tg.InputPeerClass.
func (a *Adapter) resolveInputPeer(ctx context.Context, chat domain.ChatID) (tg.InputPeerClass, error) {
	if chat.Platform() != domain.PlatformTelegram {
		return nil, fmt.Errorf("not a telegram chat: %s", chat)
	}

	// Ждём, пока адаптер инициализирует API
	select {
	case <-a.ready:
		// Всё ок, идём дальше
	case <-ctx.Done():
		return nil, ctx.Err()
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

func (a *Adapter) downloadMedia(ctx context.Context, chatID domain.ChatID, msgID int) ([]domain.Attachment, error) {
	const maxAttempts = 3

	for attempt := 0; attempt < maxAttempts; attempt++ {
		freshMsg, err := a.refreshMessage(ctx, chatID, msgID)
		if err != nil {
			return nil, fmt.Errorf("refresh message: %w", err)
		}
		if freshMsg.Media == nil {
			return nil, nil
		}

		var loc tg.InputFileLocationClass
		var attType domain.AttachmentType
		var filename string
		var mimeType string

		switch m := freshMsg.Media.(type) {
		case *tg.MessageMediaPhoto:
			photo, ok := m.Photo.AsNotEmpty()
			if !ok {
				return nil, nil
			}
			thumbSize := ""
			if len(photo.Sizes) > 1 {
				thumbSize = photo.Sizes[len(photo.Sizes)-1].GetType()
			}
			loc = &tg.InputPhotoFileLocation{
				ID:            photo.ID,
				AccessHash:    photo.AccessHash,
				FileReference: photo.FileReference,
				ThumbSize:     thumbSize,
			}
			attType = domain.AttachmentPhoto
			filename = "photo.jpg"
			mimeType = "image/jpeg" // Явно указываем MIME

		case *tg.MessageMediaDocument:
			doc, ok := m.Document.AsNotEmpty()
			if !ok {
				return nil, nil
			}
			loc = doc.AsInputDocumentFileLocation("")
			mimeType = doc.MimeType

			if strings.HasPrefix(doc.MimeType, "video/") {
				attType = domain.AttachmentVideo
				filename = "video.mp4"
			} else {
				attType = domain.AttachmentDocument
				filename = "document"
				for _, attr := range doc.Attributes {
					if fn, ok := attr.(*tg.DocumentAttributeFilename); ok {
						filename = fn.FileName
					}
				}
			}

			// ВАЖНО: Если в имени нет точки, восстанавливаем расширение по MIME
			if !strings.Contains(filename, ".") {
				ext := ".bin"
				switch mimeType {
				case "application/pdf":
					ext = ".pdf"
				case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
					ext = ".docx"
				case "application/msword":
					ext = ".doc"
				case "video/mp4":
					ext = ".mp4"
				case "image/gif":
					ext = ".gif"
				}
				filename += ext
			}

		default:
			return nil, nil
		}

		// Используем a.client.Download (простой способ)
		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			_, err := a.client.Download(loc).Stream(ctx, pw)
			if err != nil {
				pw.CloseWithError(err)
			}
		}()

		key, err := a.media.Save(ctx, pr, filename)
		if err == nil {
			return []domain.Attachment{{
				Type:     attType,
				MediaKey: key,
				FileName: filename,
				MimeType: mimeType, // <--- передаем MIME дальше
			}}, nil
		}

		if strings.Contains(err.Error(), "FILE_REFERENCE_EXPIRED") {
			a.log.Warn("file reference expired, retrying", "attempt", attempt+1)
			continue
		}

		return nil, fmt.Errorf("save media: %w", err)
	}

	return nil, fmt.Errorf("failed to download media after %d attempts", maxAttempts)
}

// refreshMessage перезапрашивает сообщение из Telegram,
// чтобы получить актуальный file_reference для медиа.
func (a *Adapter) refreshMessage(ctx context.Context, chatID domain.ChatID, msgID int) (*tg.Message, error) {
	peer, err := a.resolveInputPeer(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("resolve peer: %w", err)
	}

	var res tg.MessagesMessagesClass

	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		// Используем getHistory с OffsetID, чтобы вытащить конкретное сообщение
		req := &tg.MessagesGetHistoryRequest{
			Peer:     p,
			OffsetID: msgID + 1,
			Limit:    1,
		}
		// ВНИМАНИЕ: используем =, а не :=
		res, err = a.api.MessagesGetHistory(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("messages.getHistory (channel): %w", err)
		}
	case *tg.InputPeerChat:
		// Для групп тоже используем getHistory
		req := &tg.MessagesGetHistoryRequest{
			Peer:     p,
			OffsetID: msgID + 1,
			Limit:    1,
		}
		res, err = a.api.MessagesGetHistory(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("messages.getHistory (chat): %w", err)
		}
	default:
		return nil, fmt.Errorf("refreshMessage не поддерживает тип чата %T", peer)
	}

	// Извлекаем сообщения из результата
	var messages []tg.MessageClass
	switch v := res.(type) {
	case *tg.MessagesChannelMessages:
		messages = v.Messages
	case *tg.MessagesMessages:
		messages = v.Messages
	case *tg.MessagesMessagesSlice:
		messages = v.Messages
	default:
		return nil, fmt.Errorf("unexpected messages type: %T", res)
	}

	for _, m := range messages {
		if msg, ok := m.(*tg.Message); ok && msg.ID == msgID {
			return msg, nil
		}
	}

	return nil, fmt.Errorf("message %d not found in getHistory response", msgID)
}
