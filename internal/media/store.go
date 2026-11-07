package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
	"github.com/gabriel-vasile/mimetype"
)

type LocalStore struct {
	dir    string
	client *http.Client
}

func NewLocalStore(dir string) (*LocalStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create media dir: %w", err)
	}
	return &LocalStore{
		dir:    dir,
		client: &http.Client{Timeout: 5 * time.Minute}, // большие файлы качаются долго
	}, nil
}

func (s *LocalStore) Fetch(ctx context.Context, att domain.Attachment) (string, error) {
	// 1. Считаем хэш URL — это ключ кэша
	urlHash := sha256.Sum256([]byte(att.URL))
	cacheKey := hex.EncodeToString(urlHash[:16]) // 32 hex-символа

	// 2. Проверяем кэш: есть ли уже скачанный файл с таким префиксом
	if entries, err := os.ReadDir(s.dir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), cacheKey+"_") {
				return e.Name(), nil // кэш-хит, не качаем повторно
			}
		}
	}

	// 3. Скачиваем
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, att.URL, nil)
	if err != nil {
		return "", err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: status %d", resp.StatusCode)
	}

	// 4. Устанавливаем лимит для mimetype (чтобы заглянуть в конец ZIP-файла)
	mimetype.SetLimit(1024 * 1024) // 1 МБ

	// 5. Буфер для дублирования прочитанных байт
	var headerBuf bytes.Buffer
	tee := io.TeeReader(resp.Body, &headerBuf)

	// 6. Определяем MIME-тип по потоку
	mType, err := mimetype.DetectReader(tee)
	if err != nil {
		return "", fmt.Errorf("detect mime: %w", err)
	}

	ext := mType.Extension()
	if ext == "" {
		ext = ".bin"
	}

	// 7. Склеиваем прочитанные байты (headerBuf) с остатком (resp.Body)
	reader := io.MultiReader(&headerBuf, resp.Body)

	// 8. Формируем имя файла
	filename := att.FileName
	if filename == "" || filename == "file" || filename == "getfile" || filename == "downloaded_file" {
		switch att.Type {
		case domain.AttachmentPhoto:
			filename = "photo"
		case domain.AttachmentVideo:
			filename = "video"
		case domain.AttachmentAudio:
			filename = "audio"
		default:
			filename = "document"
		}
	}

	// Очищаем от параметров URL
	if idx := strings.IndexByte(filename, '?'); idx != -1 {
		filename = filename[:idx]
	}
	filename = filepath.Base(filename)

	// Если в имени нет точки — добавляем найденное расширение
	if !strings.Contains(filename, ".") {
		filename += ext
	}

	// 9. Ключ: <urlHash>_<filename> — это позволит кэшу найти файл при следующей правке
	key := fmt.Sprintf("%s_%s", cacheKey, filename)
	fpath := filepath.Join(s.dir, key)

	out, err := os.Create(fpath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	// 10. Сохраняем полный поток
	written, err := io.Copy(out, reader)
	if err != nil {
		os.Remove(fpath)
		return "", err
	}
	if written == 0 {
		os.Remove(fpath)
		return "", fmt.Errorf("downloaded file is empty")
	}

	return key, nil
}

// Open открывает скачанный файл для чтения.
func (s *LocalStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(s.dir, key))
}

// Remove удаляет файл по ключу.
func (s *LocalStore) Remove(ctx context.Context, key string) error {
	return os.Remove(filepath.Join(s.dir, key))
}

// Save сохраняет данные из reader в файл.
func (s *LocalStore) Save(ctx context.Context, r io.Reader, filename string) (string, error) {
	if filename == "" {
		filename = "file"
	}

	// Очищаем от URL-параметров (например, ?r=...)
	if idx := strings.IndexByte(filename, '?'); idx != -1 {
		filename = filename[:idx]
	}

	// Очищаем имя от подозрительных символов
	filename = filepath.Base(filename)

	// Добавляем таймстамп, чтобы избежать коллизий
	key := fmt.Sprintf("%d_%s", time.Now().UnixNano(), filename)
	path := filepath.Join(s.dir, key)

	out, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, r); err != nil {
		return "", fmt.Errorf("copy data: %w", err)
	}

	return key, nil
}

// GC удаляет файлы, которые старше указанного времени.
func (s *LocalStore) GC(ctx context.Context, olderThan time.Duration) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}

	cutoff := time.Now().Add(-olderThan)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(s.dir, entry.Name()))
		}
	}
	return nil
}
