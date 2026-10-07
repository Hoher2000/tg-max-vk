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
	limiter  *ratelimit.Manager
	log      *slog.Logger
}

func New(
	adapters map[string]domain.Adapter,
	routes []Route,
	store domain.Store,
	limiter *ratelimit.Manager,
	log *slog.Logger,
) *Router {
	return &Router{
		adapters: adapters,
		routes:   routes,
		store:    store,
		limiter:  limiter,
		log:      log,
	}
}

// Run запускает все адаптеры и обрабатывает входящие сообщения до отмены ctx.
func (r *Router) Run(ctx context.Context) error {
	messages := make(chan *domain.Message, 256)

	g, gctx := errgroup.WithContext(ctx)

	// Запускаем адаптеры.
	for name, adapter := range r.adapters {
		name, adapter := name, adapter
		g.Go(func() error {
			r.log.Info("adapter starting", "name", name, "platform", adapter.Platform())
			err := adapter.Start(gctx, messages)
			if err != nil && gctx.Err() == nil {
				return fmt.Errorf("adapter %s: %w", name, err)
			}
			r.log.Info("adapter stopped", "name", name)
			return nil
		})
	}

	// Запускаем обработчик сообщений.
	g.Go(func() error {
		for {
			select {
			case <-gctx.Done():
				return nil
			case msg := <-messages:
				r.handle(gctx, msg)
			}
		}
	})

	return g.Wait()
}

// handle обрабатывает одно входящее сообщение.
func (r *Router) handle(ctx context.Context, msg *domain.Message) {
	// 1. Защита от эхо-циклов.
	if msg.IsFromBridge() {
		r.log.Debug("skip bridge-originated message",
			"adapter", msg.AdapterName,
			"chat", msg.Ref.Chat.String(),
			"msg_id", msg.Ref.ID,
		)
		return
	}

	// 2. Ищем подходящие маршруты.
	matched := 0
	for _, route := range r.routes {
		if !route.matches(msg) {
			continue
		}
		matched++
		r.forward(ctx, msg, route)
	}

	if matched == 0 {
		r.log.Debug("no matching routes",
			"adapter", msg.AdapterName,
			"chat", msg.Ref.Chat.String(),
		)
	}
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

// deliver пересылает сообщение в одну конкретную цель.
func (r *Router) deliver(ctx context.Context, msg *domain.Message, route Route, target Target) error {
	adapter, ok := r.adapters[target.Adapter]
	if !ok {
		return fmt.Errorf("adapter %q not registered", target.Adapter)
	}

	// Ждём, пока можно будет отправить в этот чат.
	if err := r.limiter.Wait(ctx, target.Chat); err != nil {
		return fmt.Errorf("rate limit wait: %w", err)
	}

	// Если это reply и есть сохранённый маппинг — отвечаем как reply.
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
		// ErrNotFound — нормальная ситуация: исходное сообщение не пересылалось.
		// Падаем в обычную отправку ниже.
	}

	out := r.prepareOutgoing(msg, target)

	ref, err := adapter.Send(ctx, out)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}

	return r.saveLinkAndLog(ctx, msg, ref, route, target)
}

// tryReply пытается отправить сообщение как ответ через Replier.
func (r *Router) tryReply(ctx context.Context, adapter domain.Adapter, msg *domain.Message, target Target) (domain.MessageRef, error) {
	replier, ok := adapter.(domain.Replier)
	if !ok {
		return domain.MessageRef{}, domain.ErrUnsupported
	}

	targetChat := target.Chat

	// Ищем, куда было переслано сообщение, на которое отвечают.
	// Логика: у сообщения-источника (msg.ReplyTo) были какие-то линки.
	// Нам нужен тот, у которого Target совпадает с нашей целью.
	links, err := r.store.LookupTargets(ctx, *msg.ReplyTo)
	if err != nil {
		return domain.MessageRef{}, fmt.Errorf("lookup targets: %w", err)
	}

	for _, link := range links {
		if link.Target.Chat.Equal(targetChat) {
			// Нашли нужный target — отвечаем на него.
			out := r.prepareOutgoing(msg, target)
			return replier.Reply(ctx, link.Target, out)
		}
	}

	// Сообщение не пересылалось в целевую платформу.
	return domain.MessageRef{}, domain.ErrNotFound
}

// prepareOutgoing формирует сообщение для отправки в целевую платформу.
func (r *Router) prepareOutgoing(msg *domain.Message, target Target) *domain.Message {
	out := &domain.Message{
		Ref:         domain.MessageRef{Chat: target.Chat},
		AdapterName: target.Adapter,
		Text:        msg.Text,
		Attachments: msg.Attachments,
		Timestamp:   msg.Timestamp,
	}

	// Помечаем как созданное мостом — защита от эхо.
	out.MarkFromBridge()

	// Если не удалось передать reply — добавляем имя отправителя в текст.
	if msg.SenderName != "" {
		out.Text = fmt.Sprintf("%s:\n%s", msg.SenderName, msg.Text)
	}

	return out
}

// saveLinkAndLog сохраняет маппинг source → target и логирует.
func (r *Router) saveLinkAndLog(ctx context.Context, msg *domain.Message, targetRef domain.MessageRef, route Route, target Target) error {
	link := domain.MessageLink{
		Source: msg.Ref,
		Target: targetRef,
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
