package media

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hoher2000/tg-max-vk/internal/domain"
)

func TestLocalStore_FetchAndOpen(t *testing.T) {
	// 1. Поднимаем фейковый HTTP-сервер
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello media"))
	}))
	defer server.Close()

	// 2. Создаем хранилище во временной папке
	dir := t.TempDir()
	store, err := NewLocalStore(dir)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}

	ctx := context.Background()
	att := domain.Attachment{
		Type: domain.AttachmentDocument,
		URL:  server.URL + "/test.txt",
	}

	// 3. Скачиваем
	key, err := store.Fetch(ctx, att)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// 4. Читаем обратно
	rc, err := store.Open(ctx, key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if string(data) != "hello media" {
		t.Errorf("want 'hello media', got %q", string(data))
	}

	// 5. Удаляем
	if err := store.Remove(ctx, key); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// 6. Проверяем, что файл удален
	if _, err := store.Open(ctx, key); !os.IsNotExist(err) {
		t.Errorf("expected file to be removed")
	}
}

func TestLocalStore_GC(t *testing.T) {
	dir := t.TempDir()
	store, err := NewLocalStore(dir)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}

	// Создаём "старый" файл вручную
	oldFile := filepath.Join(dir, "old_file.tmp")
	if err := os.WriteFile(oldFile, []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Меняем его время модификации на 2 часа назад
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldFile, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// Запускаем сборщик мусора (удалять всё старше 1 часа)
	ctx := context.Background()
	if err := store.GC(ctx, time.Hour); err != nil {
		t.Fatalf("GC: %v", err)
	}

	// Проверяем, что старый файл удален
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Errorf("expected old file to be removed")
	}
}
