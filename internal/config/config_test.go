package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const validYAML = `
log:
  level: debug
  format: text
storage:
  path: ./data/bridge.db
adapters:
  tg:
    type: telegram
    api_id: 12345
    api_hash: abcdef
    session_name: telegram_main
  max_main:
    type: max
    token: max-token
routes:
  - id: mirror
    from_adapter: tg
    from_chat: "telegram:channel:-1001"
    targets:
      - adapter: max_main
        chat: "max:channel:42"
    options:
      forward_attachments: true
      forward_reply: true
`

func TestLoad_Valid(t *testing.T) {
	path := writeConfig(t, validYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("log.level: want debug, got %q", cfg.Log.Level)
	}
	if len(cfg.Adapters) != 2 {
		t.Errorf("adapters: want 2, got %d", len(cfg.Adapters))
	}
	if len(cfg.Routes) != 1 {
		t.Errorf("routes: want 1, got %d", len(cfg.Routes))
	}
}

func TestLoad_EnvSubstitution(t *testing.T) {
	t.Setenv("TEST_MAX_TOKEN", "secret-from-env")

	yaml := `
storage:
  path: ./data/bridge.db
adapters:
  max_main:
    type: max
    token: "${TEST_MAX_TOKEN}"
routes: []
`
	path := writeConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Adapters["max_main"].Token != "secret-from-env" {
		t.Errorf("token not substituted: %q", cfg.Adapters["max_main"].Token)
	}
}

func TestLoad_UnknownField(t *testing.T) {
	yaml := `
storage:
  path: ./data/bridge.db
  unknown_field: 42
adapters:
  max_main:
    type: max
    token: t
routes: []
`
	path := writeConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("want error for unknown field")
	}
}

func TestValidate_MissingStoragePath(t *testing.T) {
	yaml := `
adapters:
  max_main:
    type: max
    token: t
routes: []
`
	path := writeConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("want error for missing storage.path")
	}
}

func TestValidate_UnknownAdapterRefInRoute(t *testing.T) {
	yaml := `
storage:
  path: ./data/bridge.db
adapters:
  tg:
    type: telegram
    api_id: 123
    api_hash: h
    session_name: main
routes:
  - id: r1
    from_adapter: tg
    from_chat: "telegram:channel:-1"
    targets:
      - adapter: nonexistent
        chat: "max:channel:1"
`
	path := writeConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("want error for unknown target adapter")
	}
}

func TestValidate_BadLogLevel(t *testing.T) {
	yaml := `
log:
  level: verbose
storage:
  path: ./data/bridge.db
adapters:
  max_main:
    type: max
    token: t
routes: []
`
	path := writeConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("want error for bad log level")
	}
}

func TestValidate_TelegramMissingAPIID(t *testing.T) {
	yaml := `
storage:
  path: ./data/bridge.db
adapters:
  tg:
    type: telegram
    api_hash: h
    session_name: main
routes: []
`
	path := writeConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("want error for missing api_id")
	}
}
