package auth

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"

	// Переименовываем импорт, чтобы не конфликтовать с gotd/td/telegram
	tgadapter "github.com/Hoher2000/tg-max-vk/internal/adapters/telegram"

	_ "modernc.org/sqlite"
)

// Prompter — источник пользовательского ввода.
type Prompter interface {
	ReadCode(ctx context.Context) (string, error)
	ReadPassword(ctx context.Context) (string, error)
}

// StdinPrompter — реализация, читающая из stdin.
type StdinPrompter struct {
	In  io.Reader
	Out io.Writer
}

func NewStdinPrompter() *StdinPrompter {
	return &StdinPrompter{In: os.Stdin, Out: os.Stdout}
}

func (p *StdinPrompter) ReadCode(_ context.Context) (string, error) {
	fmt.Fprint(p.Out, "Код из Telegram/SMS: ")
	return p.readLine()
}

func (p *StdinPrompter) ReadPassword(_ context.Context) (string, error) {
	fmt.Fprint(p.Out, "2FA-пароль (Enter — если не нужен): ")
	line, err := p.readLine()
	if err != nil {
		return "", err
	}
	return line, nil
}

func (p *StdinPrompter) readLine() (string, error) {
	line, err := bufio.NewReader(p.In).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// sessionExists проверяет, есть ли непустая сессия в БД.
// Возвращает true, если в таблице telegram_sessions есть запись
// с указанным именем и непустыми данными.
func sessionExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var size int
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(LENGTH(data), 0) FROM telegram_sessions WHERE name = ?`,
		name,
	).Scan(&size)

	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query session: %w", err)
	}
	return size > 0, nil
}

// Run выполняет авторизацию.
func Run(ctx context.Context, cfg Config, p Prompter, log *slog.Logger) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	log.Info("opening database", "path", cfg.DBPath)
	db, err := openAndMigrate(ctx, cfg.DBPath, log)
	if err != nil {
		return err
	}
	defer db.Close()

	sessStorage := tgadapter.NewSessionStorageForAuth(db, cfg.SessionName)

	hasSession, err := sessionExists(ctx, db, cfg.SessionName)
	if err != nil {
		return fmt.Errorf("check session: %w", err)
	}

	// Если сессия уже есть и пользователь не просил проверить —
	// выходим сразу, без подключения к сети.
	if hasSession && !cfg.Verify {
		log.Info("session exists in database; use -verify to check it online",
			"session_name", cfg.SessionName,
		)
		fmt.Println("Authorized successfully. Session already saved to", cfg.DBPath)
		return nil
	}

	if hasSession && cfg.Verify {
		log.Info("session exists, verifying online (this requires working VPN/TUN)")
	}

	client := telegram.NewClient(cfg.APIID, cfg.APIHash, telegram.Options{
		SessionStorage: sessStorage,
	})

	log.Info("connecting to telegram",
		"api_id", cfg.APIID,
		"phone", cfg.Phone,
	)

	// Каналы для синхронизации с горутиной client.Run.
	readyCh := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		errCh <- client.Run(ctx, func(runCtx context.Context) error {
			log.Info("telegram: checking auth status")
			status, err := client.Auth().Status(runCtx)
			if err != nil {
				return fmt.Errorf("auth status: %w", err)
			}

			if status.Authorized {
				log.Info("already authorized", "user_id", status.User.ID)
				close(readyCh)
				return nil // ← завершаем client.Run
			}

			log.Info("telegram: not authorized, starting login flow")

			codeAuth := auth.CodeAuthenticatorFunc(func(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
				log.Info("telegram: waiting for code")
				code, err := p.ReadCode(ctx)
				if err != nil {
					return "", err
				}
				log.Info("telegram: code received")
				return code, nil
			})

			password := os.Getenv("TG_2FA_PASSWORD")
			if password == "" {
				log.Info("telegram: 2FA password not in env, will ask interactively if needed")
			} else {
				log.Info("telegram: using 2FA password from env")
			}

			flow := auth.NewFlow(
				auth.Constant(cfg.Phone, password, codeAuth),
				auth.SendCodeOptions{},
			)

			if err := client.Auth().IfNecessary(runCtx, flow); err != nil {
				return fmt.Errorf("authorization: %w", err)
			}

			log.Info("authorization complete")
			close(readyCh)
			return nil // ← завершаем client.Run
		})
	}()

	select {
	case <-ctx.Done():
		log.Info("auth cancelled by context")
		return nil
	case err := <-errCh:
		if err != nil {
			log.Error("telegram client failed", "err", err)
		}
		return err
	case <-readyCh:
		log.Info("telegram session saved", "path", cfg.DBPath)
		return nil
	case <-time.After(60 * time.Second):
		return fmt.Errorf(
			"telegram connection timeout after 60s; " +
				"проверь VPN/TUN/прокси или доступность api.telegram.org",
		)
	}
}
