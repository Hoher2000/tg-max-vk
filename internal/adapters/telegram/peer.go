package telegram

import (
	"fmt"
	"strconv"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
	"github.com/gotd/td/tg"
)

// channelIDToPeerID конвертирует «сырой» ChannelID из MTProto
// в привычный Bot API peer_id вида -100<channel_id>.
func channelIDToPeerID(channelID int64) int64 {
	return -(1000000000000 + channelID)
}

// peerIDToChannelID конвертирует peer_id вида -100<channel_id>
// обратно в «сырой» ChannelID для MTProto.
func peerIDToChannelID(peerID int64) (int64, error) {
	if peerID >= 0 {
		return 0, fmt.Errorf("channel peer_id must be negative, got %d", peerID)
	}
	// Отрезаем -100 и получаем сырой ID.
	raw := -peerID - 1000000000000
	if raw <= 0 {
		return 0, fmt.Errorf("invalid channel peer_id %d", peerID)
	}
	return raw, nil
}

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
		id = channelIDToPeerID(v.ChannelID)
	default:
		return domain.ChatID{}, fmt.Errorf("unknown peer type %T", p)
	}

	return domain.NewChatID(domain.PlatformTelegram, kind, strconv.FormatInt(id, 10))
}

func senderIDFromPeer(p tg.PeerClass) string {
	switch v := p.(type) {
	case *tg.PeerUser:
		return strconv.FormatInt(v.UserID, 10)
	case *tg.PeerChat:
		return strconv.FormatInt(v.ChatID, 10)
	case *tg.PeerChannel:
		return strconv.FormatInt(channelIDToPeerID(v.ChannelID), 10)
	default:
		return ""
	}
}
