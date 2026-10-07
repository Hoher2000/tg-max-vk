package auth

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

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
	if pw := os.Getenv("TG_2FA_PASSWORD"); pw != "" {
		return pw, nil
	}
	fmt.Fprint(p.Out, "2FA-пароль: ")
	return p.readLine()
}

func (p *StdinPrompter) readLine() (string, error) {
	line, err := bufio.NewReader(p.In).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// Run выполняет авторизацию.
func Run(ctx context.Context, cfg Config, p Prompter, log *slog.Logger) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	db, err := openAndMigrate(ctx, cfg.DBPath, log)
	if err != nil {
		return err
	}
	defer db.Close()

	// Используем экспортированную функцию из пакета tgadapter
	sessStorage := tgadapter.NewSessionStorageForAuth(db, cfg.SessionName)

	client := telegram.NewClient(cfg.APIID, cfg.APIHash, telegram.Options{
		SessionStorage: sessStorage,
	})

	return client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if status.Authorized {
			log.Info("already authorized", "user_id", status.User.ID)
			return nil
		}

		// Создаём CodeAuthenticator через функциональный тип
		codeAuth := auth.CodeAuthenticatorFunc(func(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
			return p.ReadCode(ctx)
		})

		// Пароль получаем заранее (пустая строка, если 2FA не нужна)
		password, err := p.ReadPassword(ctx)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}

		// В v0.162.0 auth.Constant принимает phone, password string, code CodeAuthenticator
		flow := auth.NewFlow(
			auth.Constant(cfg.Phone, password, codeAuth),
			auth.SendCodeOptions{},
		)

		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return fmt.Errorf("authorization: %w", err)
		}
		log.Info("authorization complete")
		return nil
	})
}
