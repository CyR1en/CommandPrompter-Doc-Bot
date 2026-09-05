package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/cyr1en/ref0/internal/api"
	"github.com/cyr1en/ref0/internal/discord"
	"github.com/cyr1en/ref0/internal/migrate"
	"github.com/cyr1en/ref0/internal/workerruntime"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		logger.Error("ref0_exit", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: ref0 <api|worker|discord|migrate>")
	}

	switch args[0] {
	case "api":
		if len(args) != 1 {
			return errors.New("usage: ref0 api")
		}
		return api.Run(ctx, slog.Default())
	case "migrate":
		return migrate.Run(ctx, args[1:])
	case "worker":
		if len(args) != 1 {
			return errors.New("usage: ref0 worker")
		}
		return workerruntime.Run(ctx, slog.Default())
	case "discord":
		if len(args) != 1 {
			return errors.New("usage: ref0 discord")
		}
		return discord.Run(ctx, slog.Default())
	default:
		return fmt.Errorf("unknown ref0 command %q", args[0])
	}
}
