package auth

import (
	"errors"
	"fmt"
)

// Config — параметры для запуска авторизации.
type Config struct {
	APIID       int
	APIHash     string
	Phone       string
	DBPath      string
	SessionName string
}

// Validate проверяет, что все обязательные поля заполнены.
func (c Config) Validate() error {
	if c.APIID <= 0 {
		return errors.New("api-id must be a positive integer")
	}
	if c.APIHash == "" {
		return errors.New("api-hash is required")
	}
	if c.Phone == "" {
		return errors.New("phone is required")
	}
	if c.DBPath == "" {
		return errors.New("db path is required")
	}
	if c.SessionName == "" {
		return errors.New("session name is required")
	}
	return nil
}

func (c Config) String() string {
	return fmt.Sprintf(
		"api_id=%d phone=%s db=%s session=%s",
		c.APIID, c.Phone, c.DBPath, c.SessionName,
	)
}