package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/Hoher2000/tg-max-vk/internal/app"
	"github.com/Hoher2000/tg-max-vk/internal/config"
	bridgelog "github.com/Hoher2000/tg-max-vk/internal/log"
)

var version = "dev"

func main() {
	_ = godotenv.Load(".env", ".env.local")

	fmt.Fprintln(os.Stderr, "DEBUG: LOG_LEVEL =", os.Getenv("LOG_LEVEL"))
	fmt.Fprintln(os.Stderr, "DEBUG: TG_API_ID =", os.Getenv("TG_API_ID"))

	root := &cobra.Command{
		Use:   "bridge",
		Short: "Bridge — мост между Telegram, MAX и VK",
	}

	root.AddCommand(runCmd())
	root.AddCommand(validateCmd())
	root.AddCommand(versionCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func runCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Запустить демон",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}

			log := bridgelog.New(cfg.Log)

			ctx, cancel := signal.NotifyContext(context.Background(),
				syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			log.Info("bridge starting", "version", version, "config", configPath)
			if err := app.Run(ctx, cfg, log); err != nil {
				log.Error("bridge failed", "err", err)
				return err
			}
			log.Info("bridge stopped")
			return nil
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "config.yaml", "path to config file")
	return cmd
}

func validateCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Проверить конфиг без запуска",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := config.Load(configPath); err != nil {
				cmd.PrintErrln("INVALID:", err)
				return err
			}
			cmd.Println("OK:", configPath)
			return nil
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "config.yaml", "path to config file")
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Показать версию",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Println(version)
		},
	}
}
