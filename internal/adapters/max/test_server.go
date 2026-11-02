//go:build test

package max

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

type FakeMaxServer struct {
	*httptest.Server
	mu       sync.Mutex
	messages map[string]*Message // mid -> message
	nextID   int
}

func NewFakeMaxServer(t *testing.T) *FakeMaxServer {
	s := &FakeMaxServer{
		messages: make(map[string]*Message),
		nextID:   1,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *FakeMaxServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	// Отправка сообщения
	case r.Method == http.MethodPost && r.URL.Path == "/messages":
		var req SendMessageRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mid := "mid." + strconv.Itoa(s.nextID)
		s.nextID++
		msg := &Message{
			Body: &MessageBody{Mid: mid, Text: req.Text, Attachments: req.Attachments},
		}
		s.messages[mid] = msg
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SendMessageResponse{Message: msg})

	// Редактирование
	case r.Method == http.MethodPut && r.URL.Path == "/messages":
		mid := r.URL.Query().Get("message_id")
		var req SendMessageRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if msg, ok := s.messages[mid]; ok {
			msg.Body.Text = req.Text
			if req.Attachments != nil {
				msg.Body.Attachments = req.Attachments
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(SendMessageResponse{Message: msg})
		} else {
			http.Error(w, "not found", http.StatusNotFound)
		}

	// Удаление
	case r.Method == http.MethodDelete && r.URL.Path == "/messages":
		mid := r.URL.Query().Get("message_id")
		delete(s.messages, mid)
		w.WriteHeader(http.StatusOK)

	// Long Polling (отдаем подготовленные апдейты)
	case r.Method == http.MethodGet && r.URL.Path == "/updates":
		// В реальном тесте сюда можно подкладывать Update объекты
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(UpdatesResponse{Updates: []Update{}})

	default:
		http.NotFound(w, r)
	}
}

// PushUpdate добавляет апдейт в очередь (для эмуляции событий от пользователя)
// В этом простом сервере мы просто сохраняем их и отдаем при GET /updates.
// Для тестов можно сделать более сложную логику.
