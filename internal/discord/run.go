package discord

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/database"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultDiscordDataDir     = "/app/data"
	defaultSupervisorScan     = 2 * time.Second
	defaultConversationExpiry = 30 * time.Minute
	maximumConversationExpiry = 43_200 * time.Minute
	discordDatabaseTimeout    = 5 * time.Second
)

type RuntimeConfig struct {
	poolConfig   *pgxpool.Config
	vault        *security.CredentialVault
	dataDir      string
	refreshEvery time.Duration
	idleExpiry   time.Duration
}

func RuntimeConfigFromEnvironment() (RuntimeConfig, error) {
	databaseURL, err := database.URLFromEnvironment()
	if err != nil {
		return RuntimeConfig{}, err
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return RuntimeConfig{}, errors.New("database configuration is invalid")
	}
	if poolConfig.ConnConfig.ConnectTimeout == 0 {
		poolConfig.ConnConfig.ConnectTimeout = discordDatabaseTimeout
	}
	vault, err := security.NewCredentialVault(
		os.Getenv("APP_MASTER_KEY"), os.Getenv("APP_PREVIOUS_MASTER_KEYS"),
	)
	if err != nil {
		return RuntimeConfig{}, err
	}
	refreshEvery, err := discordSeconds("DISCORD_SUPERVISOR_SCAN_SECONDS", defaultSupervisorScan)
	if err != nil {
		return RuntimeConfig{}, err
	}
	idleExpiry, err := discordMinutes("DISCORD_CONTEXT_IDLE_MINUTES", defaultConversationExpiry)
	if err != nil {
		return RuntimeConfig{}, err
	}
	dataDir := os.Getenv("APP_DATA_DIR")
	if dataDir == "" {
		dataDir = defaultDiscordDataDir
	}
	return RuntimeConfig{
		poolConfig: poolConfig, vault: vault, dataDir: dataDir,
		refreshEvery: refreshEvery, idleExpiry: idleExpiry,
	}, nil
}

func Run(ctx context.Context, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	config, err := RuntimeConfigFromEnvironment()
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config.poolConfig)
	if err != nil {
		return errors.New("initialize Discord database pool")
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		return errors.New("Discord database is unavailable")
	}
	artifacts, err := sourcefiles.NewStore(config.dataDir)
	if err != nil {
		return errors.New("configure Discord source artifacts")
	}
	engine, err := agents.NewPostgresEngine(pool, artifacts, config.vault, agents.OpenAIModelOptions{}, agents.EngineOptions{})
	if err != nil {
		return errors.New("configure Discord Agent executor")
	}
	configurations, err := NewStoreWithOptions(pool, config.vault, StoreOptions{
		Context: ContextOptions{IdleExpiry: config.idleExpiry},
	})
	if err != nil {
		return errors.New("configure Discord connection store")
	}
	handler, err := NewAnswerHandler(configurations, engine)
	if err != nil {
		return err
	}
	supervisor, err := NewSupervisor(configurations, handler, config.refreshEvery, nil)
	if err != nil {
		return err
	}
	logger.Info("discord_started", "scan_interval", config.refreshEvery)
	return supervisor.Run(ctx)
}

func discordSeconds(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0.1 || seconds > 60 {
		return 0, errors.New(name + " must be between 0.1 and 60 seconds")
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func discordMinutes(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	minutes, err := strconv.ParseInt(value, 10, 64)
	if err != nil || minutes <= 0 || minutes > int64(maximumConversationExpiry/time.Minute) {
		return 0, errors.New(name + " must be between 1 and 43200 minutes")
	}
	return time.Duration(minutes) * time.Minute, nil
}
