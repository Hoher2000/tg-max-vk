package max

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
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

	// Делаем до 15 попыток (максимум ~2 минуты ожидания)
	maxAttempts := 15
	for attempt := 0; attempt < maxAttempts; attempt++ {
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

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var out SendMessageResponse
			if err := json.Unmarshal(respBody, &out); err != nil {
				return nil, fmt.Errorf("decode response: %w", err)
			}
			return out.Message, nil
		}

		// Если ошибка связана с обработкой медиа — ждем и повторяем
		errStr := string(respBody)
		if strings.Contains(errStr, "video.not.processed") || strings.Contains(errStr, "attachment.not.ready") {
			// Экспоненциальная задержка: 1с, 2с, 4с, 8с, 16с (максимум 15с)
			backoff := time.Duration(1<<attempt) * time.Second
			if backoff > 15*time.Second {
				backoff = 15 * time.Second
			}
			if backoff < 1*time.Second {
				backoff = 1 * time.Second
			}

			slog.Warn("attachment is not processed yet, retrying", "attempt", attempt+1, "backoff", backoff)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			continue // Пробуем еще раз
		}

		// Другая ошибка — возвращаем сразу
		var apiErr ErrorResponse
		_ = json.Unmarshal(respBody, &apiErr)
		return nil, fmt.Errorf("max api error %d: %s (%s)", resp.StatusCode, apiErr.Message, apiErr.Details)
	}

	return nil, fmt.Errorf("failed to send message after %d attempts: video processing timeout", maxAttempts)
}

// uploadMedia загружает файл в MAX по схеме из двух шагов:
// 1. Получаем URL для загрузки.
// 2. Заливаем файл по этому URL и достаем токен.
func (c *Client) uploadMedia(ctx context.Context, att domain.Attachment, r io.Reader) (string, error) {
	maxType := "file"
	if att.Type == domain.AttachmentVideo {
		maxType = "video"
	} else if att.Type == domain.AttachmentPhoto {
		maxType = "image"
	} else if att.Type == domain.AttachmentAudio {
		maxType = "audio"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/uploads?type="+maxType, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("get upload url failed %d: %s", resp.StatusCode, string(body))
	}

	var uploadInfo UploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&uploadInfo); err != nil {
		return "", err
	}
	if uploadInfo.URL == "" {
		return "", fmt.Errorf("empty upload url returned")
	}

	// Запоминаем токен для видео/аудио (он приходит на первом шаге)
	videoToken := ""
	if maxType == "video" || maxType == "audio" {
		videoToken = uploadInfo.Token
	}

	// --- Шаг 2: Загружаем файл по полученному URL (ДЛЯ ВСЕХ ТИПОВ) ---
	// Читаем файл в буфер, чтобы Content-Length установился автоматически
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		return "", fmt.Errorf("read file to buffer: %w", err)
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Определяем имя и MIME
	fileName := att.FileName
	mimeType := att.MimeType

	// Если имена пустые — ставим дефолты по типу
	if fileName == "" || fileName == "file" {
		switch att.Type {
		case domain.AttachmentPhoto:
			fileName = "photo.jpg"
		case domain.AttachmentVideo:
			fileName = "video.mp4"
		case domain.AttachmentDocument:
			fileName = "document.bin"
		default:
			fileName = "file.bin"
		}
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="data"; filename="%s"`, fileName))
	h.Set("Content-Type", mimeType)

	part, err := writer.CreatePart(h)
	if err != nil {
		return "", fmt.Errorf("create form part: %w", err)
	}
	if _, err := io.Copy(part, &buf); err != nil {
		return "", fmt.Errorf("copy file data: %w", err)
	}
	writer.Close()

	uploadReq, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadInfo.URL, body)
	if err != nil {
		return "", err
	}
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	// Для видео/аудио Authorization на upload host не нужен
	if maxType == "image" || maxType == "file" {
		uploadReq.Header.Set("Authorization", c.token)
	}

	uploadResp, err := c.http.Do(uploadReq)
	if err != nil {
		return "", err
	}
	defer uploadResp.Body.Close()

	if uploadResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(uploadResp.Body)
		return "", fmt.Errorf("upload file failed %d: %s", uploadResp.StatusCode, string(respBody))
	}

	// --- Шаг 3: Извлекаем токен из ответа ---
	bodyBytes, err := io.ReadAll(uploadResp.Body)
	if err != nil {
		return "", err
	}

	// Если это видео или аудио — возвращаем токен с первого шага
	if maxType == "video" || maxType == "audio" {
		if videoToken == "" {
			return "", fmt.Errorf("video token not found in init response")
		}
		return videoToken, nil
	}

	// Для файлов и изображений парсим JSON ответа
	var finalInfo UploadResponse
	if err := json.Unmarshal(bodyBytes, &finalInfo); err == nil {
		if maxType == "file" && finalInfo.Token != "" {
			return finalInfo.Token, nil
		}
		if maxType == "image" {
			for _, photoData := range finalInfo.Photos {
				if photoData.Token != "" {
					return photoData.Token, nil
				}
			}
			if finalInfo.Token != "" {
				return finalInfo.Token, nil
			}
		}
	}

	return "", fmt.Errorf("token not found in upload response. body: %s", string(bodyBytes))
}
