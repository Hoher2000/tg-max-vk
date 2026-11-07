package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
	"github.com/Hoher2000/tg-max-vk/internal/ratelimit"
	"golang.org/x/sync/errgroup"
)

// Router принимает сообщения от всех адаптеров и пересылает их согласно правилам.
type Router struct {
	adapters map[string]domain.Adapter
	routes   []Route
	store    domain.Store
	media    domain.MediaStore
	limiter  *ratelimit.Manager
	log      *slog.Logger
}

func New(
	adapters map[string]domain.Adapter,
	routes []Route,
	store domain.Store,
	media domain.MediaStore, // <-- Добавили
	limiter *ratelimit.Manager,
	log *slog.Logger,
) *Router {
	return &Router{
		adapters: adapters,
		routes:   routes,
		store:    store,
		media:    media,
		limiter:  limiter,
		log:      log,
	}
}

// Run запускает все адаптеры и обрабатывает входящие сообщения до отмены ctx.
func (r *Router) Run(ctx context.Context) error {
	messages := make(chan *domain.Message, 256)
	g, gctx := errgroup.WithContext(ctx)

	for name, adapter := range r.adapters {
		name, adapter := name, adapter
		g.Go(func() error {
			r.log.Info("adapter starting", "name", name, "platform", adapter.Platform())
			err := adapter.Start(gctx, messages)
			if err != nil && gctx.Err() == nil {
				// Логируем, но не роняем весь мост, если упал один адаптер
				r.log.Error("adapter stopped with error", "name", name, "err", err)
			}
			return nil // не возвращаем ошибку в errgroup, чтобы не отменять другие адаптеры
		})
	}

	g.Go(func() error {
		for {
			select {
			case <-gctx.Done():
				return nil
			case msg, ok := <-messages:
				if !ok || msg == nil {
					return nil // канал закрыт
				}
				r.handle(gctx, msg)
			}
		}
	})

	return g.Wait()
}

// handle обрабатывает одно входящее сообщение.
func (r *Router) handle(ctx context.Context, msg *domain.Message) {
	if msg.IsFromBridge() {
		return
	}

	// Защита от эха: если это Create, проверяем, не наше ли это сообщение
	if msg.Action == "" || msg.Action == domain.ActionCreate {
		if srcLink, err := r.store.LookupSource(ctx, msg.Ref); err == nil && srcLink != nil {
			r.log.Debug("skip bridge-created message (echo)",
				"chat", msg.Ref.Chat.String(),
				"msg_id", msg.Ref.ID,
			)
			return
		}
	}

	action := msg.Action
	if action == "" {
		action = domain.ActionCreate
	}

	var matched bool
	for _, route := range r.routes {
		if action != domain.ActionDelete && !route.matches(msg) {
			continue
		}
		if action == domain.ActionDelete && msg.Ref.Chat.ID() != "" && !route.matches(msg) {
			continue
		}

		// Логируем получение ТОЛЬКО если сообщение подошло под маршрут
		if !matched {
			r.log.Debug("message matched route",
				"route", route.ID,
				"adapter", msg.AdapterName,
				"chat", msg.Ref.Chat.String(),
				"msg_id", msg.Ref.ID,
				"action", action,
				"text", msg.Text,
				"attachments", msg.Attachments,
			)
			matched = true
		}

		switch action {
		case domain.ActionCreate:
			r.forward(ctx, msg, route)
		case domain.ActionEdit:
			r.handleEdit(ctx, msg, route)
		case domain.ActionDelete:
			r.handleDelete(ctx, msg, route)
		}
	}

	// Убрали лог "no matching routes", чтобы не засорять консоль
}

// forward пересылает сообщение по одному маршруту во все цели.
func (r *Router) forward(ctx context.Context, msg *domain.Message, route Route) {
	for _, target := range route.Targets {
		if err := r.deliver(ctx, msg, route, target); err != nil {
			r.log.Error("delivery failed",
				"route", route.ID,
				"target_adapter", target.Adapter,
				"target_chat", target.Chat.String(),
				"err", err,
			)
		}
	}
}

// findTargetAdapter ищет адаптер и чат для конкретной цели из маршрута.
func (r *Router) findTargetAdapter(route Route, targetChat domain.ChatID) (domain.Adapter, domain.ChatID) {
	for _, t := range route.Targets {
		if t.Chat.Equal(targetChat) {
			return r.adapters[t.Adapter], t.Chat
		}
	}
	return nil, domain.ChatID{}
}

// fetchPendingMedia скачивает вложения, если у них есть URL, но нет MediaKey.
// Это нужно для сообщений из MAX, где приходят ссылки, а не готовые файлы.
func (r *Router) fetchPendingMedia(ctx context.Context, msg *domain.Message) {
	for i := range msg.Attachments {
		if msg.Attachments[i].MediaKey == "" && msg.Attachments[i].URL != "" {
			key, err := r.media.Fetch(ctx, msg.Attachments[i])
			if err != nil {
				r.log.Error("failed to fetch media from url", "url", msg.Attachments[i].URL, "err", err)
				continue
			}
			msg.Attachments[i].MediaKey = key
		}
	}
}

func (r *Router) handleEdit(ctx context.Context, msg *domain.Message, route Route) {
	r.log.Debug("handleEdit received",
		"ref", msg.Ref.Chat.String()+":"+msg.Ref.ID,
		"text_len", len(msg.Text),
		"attachments", len(msg.Attachments),
		"media_removed", msg.MediaRemoved,
	)
	// 1. Скачиваем новые вложения (если они пришли по URL)
	r.fetchPendingMedia(ctx, msg)

	// 2. Скачиваем pending-медиа (если пришло из TG)
	if len(msg.Attachments) > 0 && msg.Attachments[0].Type == "pending" {
		if fetcher, ok := r.adapters[msg.AdapterName].(domain.MediaFetcher); ok {
			if err := fetcher.FetchMedia(ctx, msg); err != nil {
				r.log.Error("failed to fetch pending media for edit", "err", err)
				msg.Attachments = nil
			}
		}
	}

	links, err := r.store.LookupTargets(ctx, msg.Ref)
	if err != nil {
		return
	}

	// Если это было целью, обновляем оригинал (Source)
	if len(links) == 0 {
		srcLink, err := r.store.LookupSource(ctx, msg.Ref)
		if err == nil && srcLink != nil {
			sourceAdapter, _ := r.findTargetAdapter(route, srcLink.Source.Chat)
			if sourceAdapter != nil {
				r.processEditForTarget(ctx, sourceAdapter, srcLink.Source, msg, *srcLink)
			}
		}
		return
	}

	// Если это был источник, обновляем цели (Target)
	for _, link := range links {
		targetAdapter, _ := r.findTargetAdapter(route, link.Target.Chat)
		if targetAdapter != nil {
			r.processEditForTarget(ctx, targetAdapter, link.Target, msg, link)
		}
	}
}

// processEditForTarget применяет изменения к конкретному адаптеру и сообщению.
func (r *Router) processEditForTarget(ctx context.Context, adapter domain.Adapter, ref domain.MessageRef, msg *domain.Message, link domain.MessageLink) {
	r.log.Debug("processEditForTarget",
		"ref", ref.Chat.String()+":"+ref.ID,
		"link.HasMedia", link.HasMedia,
		"msg.MediaRemoved", msg.MediaRemoved,
		"msg.Attachments", len(msg.Attachments),
	)
	// 1. Если медиа явно УДАЛЕНО
	if msg.MediaRemoved {
		if link.HasMedia {
			r.log.Info("media removed in source, recreating message", "msg_id", ref.ID)

			// 1. Удаляем старое — если упало, прерываемся, ничего не трогаем
			if deleter, ok := adapter.(domain.Deleter); ok {
				if err := deleter.Delete(ctx, ref); err != nil {
					r.log.Error("failed to delete old message, aborting recreate", "adapter", adapter.Name(), "err", err)
					return
				}
			}

			// 2. Отправляем новое. Если Send упал — старая связь остаётся
			// (она указывает на удалённое сообщение, но хотя бы не теряется).
			out := r.prepareOutgoing(msg, Target{Adapter: adapter.Name(), Chat: ref.Chat})
			newRef, sendErr := adapter.Send(ctx, out)
			if sendErr != nil {
				r.log.Error("failed to resend after media removal, link is now stale; manual DB recovery needed",
					"adapter", adapter.Name(), "err", sendErr)
				return
			}

			// 3. Атомарно обновляем связь
			oldLink := link
			if ref.Chat.Equal(link.Source.Chat) {
				link.Source = newRef
			} else {
				link.Target = newRef
			}
			link.HasMedia = false

			if err := r.store.MoveLink(ctx, oldLink, link); err != nil {
				r.log.Error("CRITICAL: failed to update link after recreate; message may be orphaned",
					"err", err)
				return
			}
			r.log.Info("message recreated", "chat", newRef.Chat.String(), "msg_id", newRef.ID)
		} else {
			if editor, ok := adapter.(domain.Editor); ok {
				out := r.prepareOutgoing(msg, Target{Adapter: adapter.Name(), Chat: ref.Chat})
				_ = editor.Edit(ctx, ref, out.Text)
			}
		}
		return
	}

	// 2. Если медиа ЕСТЬ (или добавилось новое)
	if len(msg.Attachments) > 0 {
		if mediaEditor, ok := adapter.(domain.MediaEditor); ok {
			// Передаем копию сообщения с маркером
			out := r.prepareOutgoing(msg, Target{Adapter: adapter.Name(), Chat: ref.Chat})
			if err := mediaEditor.EditMedia(ctx, ref, out); err != nil {
				r.log.Error("failed to edit media in-place", "adapter", adapter.Name(), "err", err)
			} else {
				r.log.Info("message media edited", "chat", ref.Chat.String(), "msg_id", ref.ID)
				link.HasMedia = true
				if err := r.store.SaveLink(ctx, link); err != nil {
					r.log.Error("failed to save link after edit", "err", err)
				}
			}
			return
		}
	}

	// 3. Просто обновление текста (с маркером ZWSP)
	if editor, ok := adapter.(domain.Editor); ok {
		out := r.prepareOutgoing(msg, Target{Adapter: adapter.Name(), Chat: ref.Chat})
		if err := editor.Edit(ctx, ref, out.Text); err != nil {
			r.log.Error("failed to edit message", "adapter", adapter.Name(), "err", err)
		} else {
			r.log.Info("message edited", "chat", ref.Chat.String(), "msg_id", ref.ID)
		}
	}
}

func (r *Router) handleDelete(ctx context.Context, msg *domain.Message, route Route) {
	// Если ChatID пустой (удаление в личке TG), ищем просто по ID сообщения
	if msg.Ref.Chat.ID() == "" {
		links, err := r.store.LookupTargets(ctx, msg.Ref) // LookupTargets уже умеет искать по одному ID
		if err == nil {
			for _, link := range links {
				if targetAdapter, _ := r.findTargetAdapter(route, link.Target.Chat); targetAdapter != nil {
					if deleter, ok := targetAdapter.(domain.Deleter); ok {
						_ = deleter.Delete(ctx, link.Target)
					}
				}
			}
		}
		return
	}
	links, err := r.store.LookupTargets(ctx, msg.Ref)
	if err != nil {
		r.log.Error("failed to lookup targets for delete", "err", err)
		return
	}

	if len(links) == 0 {
		srcLink, err := r.store.LookupSource(ctx, msg.Ref)
		if err != nil {
			if !errors.Is(err, domain.ErrNotFound) {
				r.log.Error("failed to lookup source for delete", "err", err)
			}
			return
		}
		if srcLink != nil {
			sourceAdapter, _ := r.findTargetAdapter(route, srcLink.Source.Chat)
			if sourceAdapter != nil {
				if deleter, ok := sourceAdapter.(domain.Deleter); ok {
					if err := deleter.Delete(ctx, srcLink.Source); err != nil {
						r.log.Error("failed to delete (source)", "adapter", sourceAdapter.Name(), "err", err)
					} else {
						r.log.Info("message deleted (source)", "chat", srcLink.Source.Chat.String(), "msg_id", srcLink.Source.ID)
					}
				}
			}
		}
		return
	}

	for _, link := range links {
		targetAdapter, _ := r.findTargetAdapter(route, link.Target.Chat)
		if targetAdapter == nil {
			continue
		}

		if deleter, ok := targetAdapter.(domain.Deleter); ok {
			if err := deleter.Delete(ctx, link.Target); err != nil {
				r.log.Error("failed to delete (target)", "adapter", targetAdapter.Name(), "err", err)
			} else {
				r.log.Info("message deleted (target)", "chat", link.Target.Chat.String(), "msg_id", link.Target.ID)
			}
		}
	}
}

// deliver пересылает сообщение в одну конкретную цель.
func (r *Router) deliver(ctx context.Context, msg *domain.Message, route Route, target Target) error {
	// 1. Получаем адаптер назначения
	adapter, ok := r.adapters[target.Adapter]
	if !ok {
		return fmt.Errorf("adapter %q not registered", target.Adapter)
	}

	// --- СКАЧИВАЕМ МЕДИА, ЕСЛИ ОНО ЕСТЬ ---
	if len(msg.Attachments) > 0 {
		// 1. Если есть URL (из MAX) — качаем через MediaStore
		r.fetchPendingMedia(ctx, msg)

		// 2. Если есть pending (из Telegram) — просим адаптер скачать
		if msg.Attachments[0].Type == "pending" {
			if fetcher, ok := r.adapters[msg.AdapterName].(domain.MediaFetcher); ok {
				if err := fetcher.FetchMedia(ctx, msg); err != nil {
					r.log.Error("failed to fetch media", "err", err)
					msg.Attachments = nil // Шлем хотя бы текст
				}
			}
		}
	}

	// 3. Ждём, пока можно будет отправить в этот чат (Rate Limit)
	if err := r.limiter.Wait(ctx, target.Chat); err != nil {
		return fmt.Errorf("rate limit wait: %w", err)
	}

	// 4. Обрабатываем Reply
	if msg.ReplyTo != nil && route.Options.ForwardReply {
		targetRef, err := r.tryReply(ctx, adapter, msg, target)
		if err == nil {
			return r.saveLinkAndLog(ctx, msg, targetRef, route, target)
		}
		if !errors.Is(err, domain.ErrNotFound) {
			r.log.Warn("reply attempt failed, falling back to plain send",
				"err", err,
				"route", route.ID,
			)
		}
	}

	// 5. Готовим сообщение к отправке
	out := r.prepareOutgoing(msg, target)

	// 6. Защита от пустых сообщений
	if out.Text == "" && len(out.Attachments) == 0 {
		r.log.Debug("skipping empty message", "msg_id", msg.Ref.ID)
		return nil
	}

	// 7. Отправляем
	ref, err := adapter.Send(ctx, out)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}

	// 8. Сохраняем связь
	return r.saveLinkAndLog(ctx, msg, ref, route, target)
}

// tryReply пытается отправить сообщение как ответ через Replier.
func (r *Router) tryReply(ctx context.Context, adapter domain.Adapter, msg *domain.Message, target Target) (domain.MessageRef, error) {
	replier, ok := adapter.(domain.Replier)
	if !ok {
		return domain.MessageRef{}, domain.ErrUnsupported
	}

	links, err := r.store.LookupTargets(ctx, *msg.ReplyTo)
	if err != nil {
		return domain.MessageRef{}, fmt.Errorf("lookup targets: %w", err)
	}

	for _, link := range links {
		if link.Target.Chat.Equal(target.Chat) && adapter.Name() == target.Adapter {
			out := r.prepareOutgoing(msg, target)
			return replier.Reply(ctx, link.Target, out)
		}
	}

	return domain.MessageRef{}, domain.ErrNotFound
}

// prepareOutgoing формирует сообщение для отправки в целевую платформу.
func (r *Router) prepareOutgoing(msg *domain.Message, target Target) *domain.Message {
	out := &domain.Message{
		Ref:         domain.MessageRef{Chat: target.Chat},
		AdapterName: target.Adapter,
		Text:        msg.Text, // БЕЗ \u200B
		Attachments: msg.Attachments,
		Timestamp:   msg.Timestamp,
	}
	out.MarkFromBridge()
	// УДАЛИТЬ блок с out.Text += "\u200B"

	if msg.SenderName != "" {
		out.Text = fmt.Sprintf("%s:\n%s", msg.SenderName, out.Text)
	}
	return out
}

// saveLinkAndLog сохраняет маппинг source → target и логирует.
func (r *Router) saveLinkAndLog(ctx context.Context, msg *domain.Message, targetRef domain.MessageRef, route Route, target Target) error {
	link := domain.MessageLink{
		Source:   msg.Ref,
		Target:   targetRef,
		HasMedia: len(msg.Attachments) > 0,
	}
	if err := r.store.SaveLink(ctx, link); err != nil {
		return fmt.Errorf("save link: %w", err)
	}

	r.log.Info("message forwarded",
		"route", route.ID,
		"from_adapter", msg.AdapterName,
		"from_chat", msg.Ref.Chat.String(),
		"from_msg", msg.Ref.ID,
		"to_adapter", target.Adapter,
		"to_chat", target.Chat.String(),
		"to_msg", targetRef.ID,
	)
	return nil
}
