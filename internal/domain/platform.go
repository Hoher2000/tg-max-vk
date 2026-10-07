package domain

// Platform — идентификатор мессенджера.
type Platform string

const (
	PlatformTelegram Platform = "telegram"
	PlatformMax      Platform = "max"
	PlatformVK       Platform = "vk"
)

// Known возвращает true, если платформа входит в список поддерживаемых.
func (p Platform) Known() bool {
	switch p {
	case PlatformTelegram, PlatformMax, PlatformVK:
		return true
	}
	return false
}

func (p Platform) String() string { return string(p) }