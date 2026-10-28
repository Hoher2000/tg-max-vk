package max

// Update — объект обновления от MAX API.
// В зависимости от update_type заполняются разные поля.
type Update struct {
	UpdateType string `json:"update_type"`
	Timestamp  int64  `json:"timestamp"`
	ChatID     int64  `json:"chat_id,omitempty"`
	UserID     int64  `json:"user_id,omitempty"`
	IsChannel  bool   `json:"is_channel,omitempty"`

	// message_created / message_edited
	Message *Message `json:"message,omitempty"`

	// message_callback
	Callback *Callback `json:"callback,omitempty"`

	// marker для Long Polling
	Marker int64 `json:"marker,omitempty"`
}

// UpdatesResponse — ответ на GET /updates.
type UpdatesResponse struct {
	Updates []Update `json:"updates"`
	Marker  int64    `json:"marker"`
}

// Message — объект сообщения.
type Message struct {
	Sender    *User          `json:"sender,omitempty"`
	Recipient *Recipient     `json:"recipient,omitempty"`
	Timestamp int64          `json:"timestamp"`
	Body      *MessageBody   `json:"body,omitempty"`
	Link      *LinkedMessage `json:"link,omitempty"`
}

// MessageBody — содержимое сообщения.
type MessageBody struct {
	Mid         string       `json:"mid,omitempty"`
	Seq         int64        `json:"seq,omitempty"`
	Text        string       `json:"text,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// User — пользователь или бот.
type User struct {
	UserID    int64  `json:"user_id"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
	IsBot     bool   `json:"is_bot,omitempty"`
}

// Recipient — получатель сообщения.
type Recipient struct {
	ChatID   int64  `json:"chat_id,omitempty"`
	UserID   int64  `json:"user_id,omitempty"`
	ChatType string `json:"chat_type,omitempty"` // dialog, chat, channel
}

// LinkedMessage — пересланное или ответное сообщение.
type LinkedMessage struct {
	Type   string       `json:"type,omitempty"` // reply, forward
	Sender *User        `json:"sender,omitempty"`
	Body   *MessageBody `json:"body,omitempty"`
}

// Callback — событие нажатия на кнопку.
type Callback struct {
	CallbackID string `json:"callback_id"`
	Payload    string `json:"payload,omitempty"`
	User       *User  `json:"user,omitempty"`
}

// Attachment — вложение.
type Attachment struct {
	Type    string `json:"type"`
	Payload any    `json:"payload,omitempty"`
}

// SendMessageRequest — тело POST /messages.
type SendMessageRequest struct {
	Text        string         `json:"text,omitempty"`
	Attachments []Attachment   `json:"attachments,omitempty"`
	Link        *LinkedMessage `json:"link,omitempty"` // для reply
}

// SendMessageResponse — ответ на отправку.
type SendMessageResponse struct {
	Message *Message `json:"message"`
}

// ErrorResponse — ответ с ошибкой.
type ErrorResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

type UploadResponse struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
	// Для изображений токен лежит в фото-мапе
	Photos map[string]struct {
		Token string `json:"token"`
	} `json:"photos,omitempty"`
}

type MediaPayload struct {
	Token string `json:"token"`
}
