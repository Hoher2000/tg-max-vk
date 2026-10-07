package max

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	defaultBaseURL = "https://platform-api2.max.ru"
	defaultTimeout = 90 * time.Second
)

// Client — HTTP-клиент для MAX Bot API.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(token string) *Client {
	return &Client{
		baseURL: defaultBaseURL,
		token:   token,
		http: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// getUpdates получает обновления через Long Polling.
func (c *Client) getUpdates(ctx context.Context, marker int64, timeout int) (*UpdatesResponse, error) {
	q := url.Values{}
	if marker > 0 {
		q.Set("marker", strconv.FormatInt(marker, 10))
	}
	if timeout > 0 {
		q.Set("timeout", strconv.Itoa(timeout))
	}
	q.Set("limit", "100")

	endpoint := c.baseURL + "/updates?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var out UpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode updates: %w", err)
	}
	return &out, nil
}

// sendMessage отправляет сообщение в чат или пользователю.
// chatID > 0 — в чат, userID > 0 — пользователю.
func (c *Client) sendMessage(ctx context.Context, chatID, userID int64, body SendMessageRequest) (*Message, error) {
	q := url.Values{}
	if chatID != 0 {
		q.Set("chat_id", strconv.FormatInt(chatID, 10))
	} else if userID != 0 {
		q.Set("user_id", strconv.FormatInt(userID, 10))
	} else {
		return nil, fmt.Errorf("either chatID or userID must be set")
	}

	endpoint := c.baseURL + "/messages?" + q.Encode()

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr ErrorResponse
		_ = json.Unmarshal(respBody, &apiErr)
		return nil, fmt.Errorf("max api error %d: %s (%s)", resp.StatusCode, apiErr.Message, apiErr.Details)
	}

	var out SendMessageResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out.Message, nil
}

// replyToMessage отправляет ответ на сообщение через поле link.
func (c *Client) replyToMessage(ctx context.Context, chatID, userID int64, text string, replyToMID string) (*Message, error) {
	body := SendMessageRequest{
		Text: text,
		Link: &LinkedMessage{
			Type: "reply",
			Body: &MessageBody{Mid: replyToMID},
		},
	}
	return c.sendMessage(ctx, chatID, userID, body)
}
