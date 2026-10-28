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

	// 1. Читаем первые 512 байт для определения типа (магия файлов)
	buffer := make([]byte, 512)
	n, _ := io.ReadFull(resp.Body, buffer)

	// http.DetectContentType понимает PDF, ZIP (в т.ч. DOCX/XLSX), картинки и т.д.
	detectedMime := http.DetectContentType(buffer[:n])

	// Возвращаем считанные байты обратно в поток
	reader := io.MultiReader(bytes.NewReader(buffer[:n]), resp.Body)

	// 2. Определяем расширение
	ext := ""
	switch {
	case strings.Contains(detectedMime, "application/pdf"):
		ext = ".pdf"
	case strings.Contains(detectedMime, "application/zip"): // DOCX/XLSX это zip
		ext = ".zip" // Поставим zip, если MAX не передал точное имя
	case strings.Contains(detectedMime, "image/jpeg"):
		ext = ".jpg"
	case strings.Contains(detectedMime, "image/png"):
		ext = ".png"
	case strings.Contains(detectedMime, "image/webp"):
		ext = ".webp"
	case strings.Contains(detectedMime, "video/mp4"):
		ext = ".mp4"
	}

	// 3. Формируем имя файла
	filename := att.FileName
	if filename == "" || filename == "file" || filename == "getfile" || filename == "downloaded_file" {
		filename = "document"
	}

	// Очищаем от мусора
	if idx := strings.IndexByte(filename, '?'); idx != -1 {
		filename = filename[:idx]
	}
	filename = filepath.Base(filename)

	// Если в имени нет точки — добавляем найденное расширение
	if !strings.Contains(filename, ".") && ext != "" {
		filename += ext
	} else if !strings.Contains(filename, ".") && ext == "" {
		filename += ".bin"
	}

	key := fmt.Sprintf("%d_%s", time.Now().UnixNano(), filename)
	fpath := filepath.Join(s.dir, key)

	out, err := os.Create(fpath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	written, err := io.Copy(out, reader)
	if err != nil {
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
