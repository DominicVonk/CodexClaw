package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/DominicVonk/CodexClaw/internal/codexapp"
	"github.com/DominicVonk/CodexClaw/internal/config"
	"github.com/DominicVonk/CodexClaw/internal/router"
	"github.com/DominicVonk/CodexClaw/internal/transport/telegram"
	"github.com/DominicVonk/CodexClaw/internal/transport/whatsapp"
	"golang.org/x/sync/errgroup"
)

var (
	version = "0.0.0-alpha.1"
	commit  = "dev"
	date    = "unknown"
)

func main() {
	if err := run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		return usage()
	}

	switch args[1] {
	case "serve":
		return serve(args[2:])
	case "whatsapp-login":
		return whatsappLogin(args[2:])
	case "version", "-v", "--version":
		printVersion()
		return nil
	case "help", "-h", "--help":
		return usage()
	default:
		return fmt.Errorf("unknown command %q", args[1])
	}
}

func usage() error {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  codexclaw serve -config config.yml")
	fmt.Fprintln(os.Stderr, "  codexclaw whatsapp-login -config config.yml [-phone 31612345678]")
	fmt.Fprintln(os.Stderr, "  codexclaw version")
	return errors.New("missing command")
}

func printVersion() {
	fmt.Printf("codexclaw %s\ncommit: %s\nbuilt: %s\n", version, commit, date)
}

func loadConfig(args []string) (config.Config, error) {
	fs := flag.NewFlagSet("codexclaw", flag.ContinueOnError)
	path := fs.String("config", "", "path to YAML config; defaults to config.yml or config.yaml")
	if err := fs.Parse(args); err != nil {
		return config.Config{}, err
	}
	return config.Load(*path)
}

func serve(args []string) error {
	cfg, err := loadConfig(args)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gateway, err := codexapp.Start(ctx, cfg.Codex)
	if err != nil {
		return err
	}
	defer gateway.Close()

	rt, err := router.New(gateway, cfg)
	if err != nil {
		return err
	}
	defer rt.Close()

	group, ctx := errgroup.WithContext(ctx)

	if cfg.TelegramActive() {
		group.Go(func() error {
			return telegram.Run(ctx, cfg.Telegram, cfg.Media, rt)
		})
	}
	if cfg.WhatsAppActive() {
		group.Go(func() error {
			return whatsapp.Run(ctx, cfg.WhatsApp, cfg.Media, rt)
		})
	}
	if !cfg.TelegramActive() && !cfg.WhatsAppActive() {
		return errors.New("no transports enabled")
	}

	return group.Wait()
}

func whatsappLogin(args []string) error {
	fs := flag.NewFlagSet("codexclaw whatsapp-login", flag.ContinueOnError)
	path := fs.String("config", "", "path to YAML config; defaults to config.yml or config.yaml")
	phone := fs.String("phone", "", "international phone number for WhatsApp pairing code")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *phone != "" {
		return whatsapp.PairPhone(ctx, cfg.WhatsApp, *phone)
	}
	return whatsapp.Login(ctx, cfg.WhatsApp)
}
