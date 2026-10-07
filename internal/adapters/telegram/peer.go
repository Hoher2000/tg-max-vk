package telegram

import (
	"fmt"
	"strconv"

	"github.com/gotd/td/tg"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
)

// chatIDFromPeer строит domain.ChatID из tg.PeerClass.
func chatIDFromPeer(p tg.PeerClass) (domain.ChatID, error) {
	var kind domain.ChatKind
	var id int64

	switch v := p.(type) {
	case *tg.PeerUser:
		kind = domain.ChatKindUser
		id = v.UserID
	case *tg.PeerChat:
		kind = domain.ChatKindGroup
		id = v.ChatID
	case *tg.PeerChannel:
		kind = domain.ChatKindChannel
		id = v.ChannelID
	default:
		return domain.ChatID{}, fmt.Errorf("unknown peer type %T", p)
	}

	return domain.NewChatID(domain.PlatformTelegram, kind, strconv.FormatInt(id, 10))
}

// senderIDFromPeer строит строковый ID отправителя (тот же формат).
func senderIDFromPeer(p tg.PeerClass) string {
	switch v := p.(type) {
	case *tg.PeerUser:
		return strconv.FormatInt(v.UserID, 10)
	case *tg.PeerChat:
		return strconv.FormatInt(v.ChatID, 10)
	case *tg.PeerChannel:
		return strconv.FormatInt(v.ChannelID, 10)
	default:
		return ""
	}
}
