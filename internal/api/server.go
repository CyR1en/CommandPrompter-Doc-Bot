package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/artifacts"
	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/chattokens"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/discord"
	docgen "github.com/cyr1en/ref0/internal/documentation"
	"github.com/cyr1en/ref0/internal/events"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/jobterminal"
	"github.com/cyr1en/ref0/internal/knowledgebases"
	"github.com/cyr1en/ref0/internal/operations"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/cyr1en/ref0/internal/sources"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Run starts the API and drains in-flight requests when ctx is cancelled.
func Run(ctx context.Context, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	config, err := ConfigFromEnvironment()
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config.poolConfig)
	if err != nil {
		return errors.New("initialize database pool")
	}
	defer pool.Close()
	config.metricsReader = &databaseMetricsReader{pool: pool}
	sessionService, err := auth.NewService(pool, config.sessionTTL, auth.DefaultPasswordConcurrency)
	if err != nil {
		return errors.New("configure operator sessions")
	}
	if err = sessionService.InitializeBootstrap(
		ctx,
		config.bootstrapToken,
		config.bootstrapTokenTTL,
	); err != nil {
		return errors.New("initialize operator bootstrap")
	}
	eventReader, err := events.NewReader(pool)
	if err != nil {
		return errors.New("configure event reader")
	}
	jobService, err := jobs.NewService(pool, config.vault, jobterminal.Callback)
	if err != nil {
		return errors.New("configure job service")
	}
	operationsService, err := operations.NewService(pool)
	if err != nil {
		return errors.New("configure operations service")
	}
	credentialService, err := credentials.NewService(pool, config.vault)
	if err != nil {
		return errors.New("configure credential service")
	}
	artifactPurger, err := artifacts.NewKnowledgeBasePurger(config.dataDir)
	if err != nil {
		return errors.New("configure artifact purge service")
	}
	knowledgeBaseService, err := knowledgebases.NewService(pool, config.vault, config.deleteGrace, artifactPurger)
	if err != nil {
		return errors.New("configure knowledge base service")
	}
	providerRuntime, err := providers.NewRuntime(pool, config.vault, providers.ExecutionOptions{})
	if err != nil {
		return errors.New("configure provider service")
	}
	sourceStore, err := sources.NewStore(pool, jobService.Queue(), config.vault)
	if err != nil {
		return errors.New("configure source service")
	}
	discordStore, err := discord.NewStore(pool, config.vault)
	if err != nil {
		return errors.New("configure Discord service")
	}
	sourceArtifacts, err := sourcefiles.NewStore(config.dataDir)
	if err != nil {
		return errors.New("configure source artifact service")
	}
	runArtifacts, err := artifacts.NewRunStore(config.dataDir)
	if err != nil {
		return errors.New("configure documentation run artifacts")
	}
	wikiArtifacts, err := artifacts.NewWikiStore(config.dataDir)
	if err != nil {
		return errors.New("configure wiki artifacts")
	}
	documentationStore, err := docgen.NewStore(pool, jobService.Queue(), config.vault, runArtifacts, wikiArtifacts, sourceArtifacts)
	if err != nil {
		return errors.New("configure documentation service")
	}
	agentCatalog, err := agents.NewCatalog(pool, config.vault)
	if err != nil {
		return errors.New("configure Agent catalog")
	}
	chatTokenService, err := chattokens.NewService(pool, config.vault)
	if err != nil {
		return errors.New("configure chat access tokens")
	}
	agentEngine, err := agents.NewPostgresEngine(
		pool, sourceArtifacts, config.vault, agents.OpenAIModelOptions{}, agents.EngineOptions{},
	)
	if err != nil {
		return errors.New("configure Agent executor")
	}
	compatibility, err := newCompatibilityHandler(chatTokenService, agentCatalog.Store, agentEngine, nil)
	if err != nil {
		return errors.New("configure chat compatibility API")
	}
	routes := controlPlaneRoutes{
		sessions: sessionService, credentials: credentialService,
		knowledgeBases: knowledgeBaseService, providers: providerRuntime.Store,
		sources: sourceStore, discord: discordStore, jobs: jobService,
		documentation: documentationStore, documentationJobs: jobService,
		agents: agentCatalog, chatTokens: chatTokenService,
	}

	registry := prometheus.NewRegistry()
	handler, err := newHandlerWithRaw(
		config,
		readinessProbe{pool: pool, dataDir: config.dataDir, masterKey: config.vault != nil},
		sessionService,
		eventReader,
		jobService,
		operationsService,
		registry,
		registry,
		logger,
		compatibility.register,
		routes.register,
	)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              config.address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", config.address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", config.address, err)
	}

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()
	logger.Info("api_started", "address", config.address, "version", config.version)

	select {
	case err = <-serveResult:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve API: %w", err)
	case <-ctx.Done():
		logger.Info("api_stopping")
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
	defer cancel()
	if err = server.Shutdown(shutdownContext); err != nil {
		_ = server.Close()
		return errors.New("API graceful shutdown timed out")
	}
	if err = <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve API: %w", err)
	}
	return nil
}
