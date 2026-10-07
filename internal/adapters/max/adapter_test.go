package max

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
)

func newTestAdapter(t *testing.T, serverURL string) *Adapter {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := New(Config{Name: "max_test", Token: "test-token"}, log)
	a.client.baseURL = serverURL
	return a
}

func TestConvertUpdate_MessageCreated(t *testing.T) {
	a := newTestAdapter(t, "")

	u := Update{
		UpdateType: "message_created",
		Message: &Message{
			Sender: &User{
				UserID:    42,
				FirstName: "Вася",
				LastName:  "Пупкин",
			},
			Recipient: &Recipient{
				ChatID:   -100123,
				ChatType: "channel",
			},
			Timestamp: 1700000000000,
			Body: &MessageBody{
				Mid:  "msg-1",
				Text: "привет",
			},
		},
	}

	msg := a.convertUpdate(u)
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.Ref.Chat.Platform() != domain.PlatformMax {
		t.Errorf("platform: want max, got %s", msg.Ref.Chat.Platform())
	}
	if msg.Ref.Chat.Kind() != domain.ChatKindChannel {
		t.Errorf("kind: want channel, got %s", msg.Ref.Chat.Kind())
	}
	if msg.Ref.ID != "msg-1" {
		t.Errorf("id: want msg-1, got %s", msg.Ref.ID)
	}
	if msg.Text != "привет" {
		t.Errorf("text: want привет, got %s", msg.Text)
	}
	if msg.SenderName != "Вася Пупкин" {
		t.Errorf("sender name: want Вася Пупкин, got %s", msg.SenderName)
	}
	if msg.AdapterName != "max_test" {
		t.Errorf("adapter name mismatch")
	}
}

func TestConvertUpdate_IgnoresNonMessage(t *testing.T) {
	a := newTestAdapter(t, "")

	u := Update{UpdateType: "bot_started"}
	if got := a.convertUpdate(u); got != nil {
		t.Errorf("expected nil for non-message update, got %+v", got)
	}
}

func TestParseTargetChat(t *testing.T) {
	tests := []struct {
		name       string
		chat       domain.ChatID
		wantChatID int64
		wantUserID int64
		wantErr    bool
	}{
		{
			name:       "user",
			chat:       mustChat(t, domain.ChatKindUser, "42"),
			wantChatID: 0,
			wantUserID: 42,
		},
		{
			name:       "channel",
			chat:       mustChat(t, domain.ChatKindChannel, "-100123"),
			wantChatID: -100123,
			wantUserID: 0,
		},
		{
			name:       "group",
			chat:       mustChat(t, domain.ChatKindGroup, "-200"),
			wantChatID: -200,
			wantUserID: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chatID, userID, err := parseTargetChat(tt.chat)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err: want %v, got %v", tt.wantErr, err)
			}
			if chatID != tt.wantChatID {
				t.Errorf("chatID: want %d, got %d", tt.wantChatID, chatID)
			}
			if userID != tt.wantUserID {
				t.Errorf("userID: want %d, got %d", tt.wantUserID, userID)
			}
		})
	}
}

func mustChat(t *testing.T, kind domain.ChatKind, id string) domain.ChatID {
	t.Helper()
	c, err := domain.NewChatID(domain.PlatformMax, kind, id)
	if err != nil {
		t.Fatalf("NewChatID: %v", err)
	}
	return c
}

func TestClient_SendMessage(t *testing.T) {
	var gotAuth string
	var gotQuery string
	var gotBody SendMessageRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		resp := SendMessageResponse{
			Message: &Message{
				Body: &MessageBody{Mid: "sent-1", Text: gotBody.Text},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient("secret")
	client.baseURL = server.URL

	msg, err := client.sendMessage(context.Background(), -100123, 0, SendMessageRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if msg.Body.Mid != "sent-1" {
		t.Errorf("mid: want sent-1, got %s", msg.Body.Mid)
	}
	if gotAuth != "secret" {
		t.Errorf("auth header: want secret, got %s", gotAuth)
	}
	if gotQuery != "chat_id=-100123" {
		t.Errorf("query: want chat_id=-100123, got %s", gotQuery)
	}
	if gotBody.Text != "hello" {
		t.Errorf("body text: want hello, got %s", gotBody.Text)
	}
}

func TestClient_SendMessage_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(ErrorResponse{
			Code:    401,
			Message: "invalid token",
		})
	}))
	defer server.Close()

	client := NewClient("bad")
	client.baseURL = server.URL

	_, err := client.sendMessage(context.Background(), -100123, 0, SendMessageRequest{Text: "hi"})
	if err == nil {
		t.Fatal("want error")
	}
}
