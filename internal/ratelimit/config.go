package ratelimit

import (
	"time"

	"golang.org/x/time/rate"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
)

// Limits — параметры ограничений для одной платформы.
type Limits struct {
	// Global — сколько сообщений в секунду можно отправлять суммарно
	// по всем чатам платформы.
	Global rate.Limit

	// GlobalBurst — размер всплеска на глобальном лимите.
	GlobalBurst int

	// PerChat — сколько сообщений в секунду можно отправлять в один чат.
	PerChat rate.Limit

	// PerChatBurst — размер всплеска на лимите чата.
	PerChatBurst int

	// IdleTimeout — через сколько времени неиспользуемый лимитер чата
	// удаляется из памяти.
	IdleTimeout time.Duration
}

// DefaultLimits возвращает рекомендованные лимиты для платформы.
// Значения подобраны с запасом, чтобы не ловить 429 от API.
func DefaultLimits(p domain.Platform) Limits {
	switch p {
	case domain.PlatformMax:
		// MAX: 2 msg/sec в канал (жёстко по документации).
		return Limits{
			Global:       rate.Limit(10),
			GlobalBurst:  5,
			PerChat:      rate.Limit(2),
			PerChatBurst: 1,
			IdleTimeout:  10 * time.Minute,
		}
	case domain.PlatformTelegram:
		// Telegram: 30 msg/sec глобально, 1 msg/sec в чат.
		return Limits{
			Global:       rate.Limit(25),
			GlobalBurst:  5,
			PerChat:      rate.Limit(1),
			PerChatBurst: 1,
			IdleTimeout:  10 * time.Minute,
		}
	case domain.PlatformVK:
		// VK: 20 msg/sec глобально, 1 msg/sec в беседу.
		return Limits{
			Global:       rate.Limit(18),
			GlobalBurst:  5,
			PerChat:      rate.Limit(1),
			PerChatBurst: 1,
			IdleTimeout:  10 * time.Minute,
		}
	default:
		// Неизвестная платформа — очень консервативно.
		return Limits{
			Global:       rate.Limit(5),
			GlobalBurst:  2,
			PerChat:      rate.Limit(1),
			PerChatBurst: 1,
			IdleTimeout:  10 * time.Minute,
		}
	}
}
