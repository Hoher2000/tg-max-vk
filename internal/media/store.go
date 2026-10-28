package media

import (
	"bytes"
	"context"
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

	// 1. Устанавливаем лимит для mimetype (чтобы заглянуть в конец ZIP-файла)
	mimetype.SetLimit(1024 * 1024) // 1 МБ

	// 2. Буфер для дублирования прочитанных байт
	var headerBuf bytes.Buffer
	tee := io.TeeReader(resp.Body, &headerBuf)

	// 3. Определяем MIME-тип по потоку
	mType, err := mimetype.DetectReader(tee)
	if err != nil {
		return "", fmt.Errorf("detect mime: %w", err)
	}

	ext := mType.Extension()
	if ext == "" {
		ext = ".bin"
	}

	// 4. Склеиваем прочитанные байты (headerBuf) с остатком (resp.Body)
	reader := io.MultiReader(&headerBuf, resp.Body)

	// 5. Формируем имя файла
	filename := att.FileName
	if filename == "" || filename == "file" || filename == "getfile" || filename == "downloaded_file" {
		filename = "document"
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

	key := fmt.Sprintf("%d_%s", time.Now().UnixNano(), filename)
	fpath := filepath.Join(s.dir, key)

	out, err := os.Create(fpath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	// 6. Сохраняем полный поток
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
