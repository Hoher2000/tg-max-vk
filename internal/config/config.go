package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config — корневая конфигурация Bridge.
type Config struct {
	Log      LogConfig                `yaml:"log"`
	Storage  StorageConfig            `yaml:"storage"`
	Adapters map[string]AdapterConfig `yaml:"adapters"`
	Routes   []RouteConfig            `yaml:"routes"`
}

type LogConfig struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // text | json
}

type StorageConfig struct {
	Path string `yaml:"path"`
}

// AdapterConfig — параметры одного инстанса адаптера.
// Поля зависят от Type: у telegram — api_id, api_hash, session_name;
// у max — token; у vk — token.
type AdapterConfig struct {
	Type string `yaml:"type"`

	// Telegram
	APIID       int    `yaml:"api_id"`
	APIHash     string `yaml:"api_hash"`
	SessionName string `yaml:"session_name"`

	// MAX / VK
	Token string `yaml:"token"`
}

type RouteConfig struct {
	ID          string            `yaml:"id"`
	FromAdapter string            `yaml:"from_adapter"`
	FromChat    string            `yaml:"from_chat"`
	Targets     []TargetConfig    `yaml:"targets"`
	Options     RouteOptionsConfig `yaml:"options"`
}

type TargetConfig struct {
	Adapter string `yaml:"adapter"`
	Chat    string `yaml:"chat"`
}

type RouteOptionsConfig struct {
	ForwardAttachments bool `yaml:"forward_attachments"`
	ForwardReply       bool `yaml:"forward_reply"`
	OnlyText           bool `yaml:"only_text"`
}

// Load читает YAML, подставляет ${ENV_VAR}, валидирует.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	expanded := os.Expand(string(data), func(key string) string {
		return os.Getenv(key)
	})

	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true) // ругаться на неизвестные поля — защита от опечаток

	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate проверяет конфиг на типичные ошибки.
func (c *Config) Validate() error {
	if c.Storage.Path == "" {
		return errors.New("storage.path is required")
	}

	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "text"
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level: unknown value %q", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("log.format: unknown value %q", c.Log.Format)
	}

	if len(c.Adapters) == 0 {
		return errors.New("at least one adapter is required")
	}
	for name, a := range c.Adapters {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("adapter %q: %w", name, err)
		}
	}

	for i, r := range c.Routes {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("routes[%d]: %w", i, err)
		}
		if _, ok := c.Adapters[r.FromAdapter]; !ok {
			return fmt.Errorf("routes[%d]: from_adapter %q not defined", i, r.FromAdapter)
		}
		for j, t := range r.Targets {
			if _, ok := c.Adapters[t.Adapter]; !ok {
				return fmt.Errorf("routes[%d].targets[%d]: adapter %q not defined", i, j, t.Adapter)
			}
		}
	}
	return nil
}

func (a AdapterConfig) Validate() error {
	if a.Type == "" {
		return errors.New("type is required")
	}
	switch a.Type {
	case "telegram":
		if a.APIID <= 0 {
			return errors.New("api_id must be positive")
		}
		if a.APIHash == "" {
			return errors.New("api_hash is required")
		}
		if a.SessionName == "" {
			return errors.New("session_name is required")
		}
	case "max", "vk":
		if a.Token == "" {
			return errors.New("token is required")
		}
	default:
		return fmt.Errorf("unknown type %q", a.Type)
	}
	return nil
}

func (r RouteConfig) Validate() error {
	if r.ID == "" {
		return errors.New("id is required")
	}
	if r.FromAdapter == "" {
		return errors.New("from_adapter is required")
	}
	if r.FromChat == "" {
		return errors.New("from_chat is required")
	}
	if len(r.Targets) == 0 {
		return errors.New("at least one target is required")
	}
	for i, t := range r.Targets {
		if t.Adapter == "" {
			return fmt.Errorf("targets[%d]: adapter is required", i)
		}
		if t.Chat == "" {
			return fmt.Errorf("targets[%d]: chat is required", i)
		}
	}
	return nil
}