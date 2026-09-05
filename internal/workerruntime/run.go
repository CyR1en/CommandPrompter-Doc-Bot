// Package workerruntime composes the worker process without making the worker
// loop depend on the domains whose handlers it dispatches.
package workerruntime

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/cyr1en/ref0/internal/artifacts"
	"github.com/cyr1en/ref0/internal/capsule"
	"github.com/cyr1en/ref0/internal/capsuledoc"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/database"
	"github.com/cyr1en/ref0/internal/discord"
	docgen "github.com/cyr1en/ref0/internal/documentation"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/jobterminal"
	"github.com/cyr1en/ref0/internal/knowledgebases"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/retention"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/cyr1en/ref0/internal/sources"
	"github.com/cyr1en/ref0/internal/worker"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	databaseConnectTimeout = 5 * time.Second
	capsuleCloseTimeout    = 10 * time.Second
)

type configuration struct {
	runner   worker.Config
	runtime  worker.RuntimeConfig
	sources  sources.RuntimeConfig
	database string
	vault    *security.CredentialVault
}

func configurationFromEnvironment() (configuration, error) {
	runnerConfig, err := worker.ConfigFromEnvironment()
	if err != nil {
		return configuration{}, err
	}
	runtimeConfig, err := worker.RuntimeConfigFromEnvironment()
	if err != nil {
		return configuration{}, err
	}
	sourceConfig, err := sources.RuntimeConfigFromEnvironment()
	if err != nil {
		return configuration{}, err
	}
	databaseURL, err := database.URLFromEnvironment()
	if err != nil {
		return configuration{}, err
	}
	vault, err := security.NewCredentialVault(
		os.Getenv("APP_MASTER_KEY"),
		os.Getenv("APP_PREVIOUS_MASTER_KEYS"),
	)
	if err != nil {
		return configuration{}, err
	}
	return configuration{
		runner: runnerConfig, runtime: runtimeConfig, sources: sourceConfig,
		database: databaseURL, vault: vault,
	}, nil
}

func capsuleSlots(paths [2]string) ([]capsule.Slot, error) {
	names := [...]string{"slot-0", "slot-1"}
	slots := make([]capsule.Slot, len(paths))
	for index, path := range paths {
		slot, err := capsule.NewSlot(names[index], path)
		if err != nil {
			return nil, err
		}
		slots[index] = slot
	}
	return slots, nil
}

func newCapsulePool(paths [2]string) (*capsule.SlotPool, error) {
	slots, err := capsuleSlots(paths)
	if err != nil {
		return nil, err
	}
	return capsule.NewSlotPool(slots)
}

// Run starts the complete durable worker. The capsule pool is validated before
// a database pool is created, so a malformed isolation boundary cannot claim a
// job and then fail during documentation execution.
func Run(ctx context.Context, logger *slog.Logger) (returnedError error) {
	if logger == nil {
		logger = slog.Default()
	}
	config, err := configurationFromEnvironment()
	if err != nil {
		return err
	}
	slotPool, err := newCapsulePool(config.runtime.CapsuleSocketPaths)
	if err != nil {
		return errors.New("capsule slot pool is unavailable")
	}
	var databasePool *pgxpool.Pool
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), capsuleCloseTimeout)
		defer cancel()
		if closeErr := slotPool.Close(closeCtx); closeErr != nil && returnedError == nil {
			returnedError = errors.New("capsule slot pool did not close")
		}
		if databasePool != nil {
			databasePool.Close()
		}
	}()
	if err = slotPool.Start(); err != nil {
		return errors.New("capsule slot pool is unavailable")
	}

	poolConfig, err := pgxpool.ParseConfig(config.database)
	if err != nil {
		return errors.New("database configuration is invalid")
	}
	if poolConfig.ConnConfig.ConnectTimeout == 0 {
		poolConfig.ConnConfig.ConnectTimeout = databaseConnectTimeout
	}
	databasePool, err = pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("database pool configuration is invalid")
	}
	if err = databasePool.Ping(ctx); err != nil {
		return errors.New("database is unavailable")
	}

	services, err := compose(databasePool, slotPool, config, logger)
	if err != nil {
		return err
	}
	logger.Info("worker_started", "worker_id", config.runner.WorkerID)
	return runServices(ctx, services...)
}

type service func(context.Context) error

func compose(
	pool *pgxpool.Pool,
	slotPool *capsule.SlotPool,
	config configuration,
	logger *slog.Logger,
) ([]service, error) {
	sourceArtifacts, err := sourcefiles.NewStore(config.runtime.DataDir)
	if err != nil {
		return nil, errors.New("configure source artifacts")
	}
	secretReader, err := credentials.NewSecretReader(pool, config.vault)
	if err != nil {
		return nil, err
	}
	providerRuntime, err := providers.NewRuntime(pool, config.vault, providers.ExecutionOptions{})
	if err != nil {
		return nil, errors.New("configure provider runtime")
	}
	agentRuntime, err := capsuledoc.NewRuntime(
		providerRuntime.Store,
		sourceArtifacts,
		slotPool,
		secretReader,
		config.runtime.ApplicationVersion,
		capsuledoc.DefaultOptions(),
	)
	if err != nil {
		return nil, errors.New("configure documentation capsule runtime")
	}
	queue := jobs.NewStore(pool, jobterminal.Callback)
	documentationRuntime, err := docgen.NewRuntime(
		pool, queue, config.vault, config.runtime.DataDir, agentRuntime,
	)
	if err != nil {
		return nil, errors.New("configure documentation runtime")
	}

	sourceStore, sourceRegistry, err := composeSources(
		pool, documentationRuntime.Queue, config.vault, secretReader,
		sourceArtifacts, config.sources,
	)
	if err != nil {
		return nil, err
	}
	discordStore, err := discord.NewStore(pool, config.vault)
	if err != nil {
		return nil, errors.New("configure Discord store")
	}
	discordRegistry, err := discord.WorkerHandlers(discordStore, secretReader, nil)
	if err != nil {
		return nil, err
	}
	artifactPurger, err := artifacts.NewKnowledgeBasePurger(config.runtime.DataDir)
	if err != nil {
		return nil, errors.New("configure knowledge base artifact purge")
	}
	knowledgeBaseService, err := knowledgebases.NewService(
		pool, config.vault, config.runtime.DeleteGrace, artifactPurger,
	)
	if err != nil {
		return nil, errors.New("configure knowledge base purge")
	}
	knowledgeBaseRegistry, err := knowledgebases.Handlers(knowledgeBaseService)
	if err != nil {
		return nil, err
	}
	retentionService, err := retention.NewService(
		pool,
		retention.Policy{
			SourceSnapshots: config.runtime.RetentionPolicy.SourceSnapshots,
			FailedDrafts:    config.runtime.RetentionPolicy.FailedDrafts,
			JobLogs:         config.runtime.RetentionPolicy.JobLogs,
			EventLog:        config.runtime.RetentionPolicy.EventLog,
			AgentRuns:       config.runtime.RetentionPolicy.AgentRuns,
			DiscordContext:  config.runtime.RetentionPolicy.DiscordContext,
			OldWikis:        config.runtime.RetentionPolicy.OldWikis,
			BatchSize:       config.runtime.RetentionPolicy.BatchSize,
		},
		sourceArtifacts,
		documentationRuntime.RunArtifacts,
		documentationRuntime.WikiArtifacts,
	)
	if err != nil {
		return nil, errors.New("configure retention")
	}
	retentionRegistry, err := retention.Handlers(retentionService)
	if err != nil {
		return nil, err
	}
	providerRegistry, err := adaptProviderRegistry(providerRuntime.Handlers.Registry())
	if err != nil {
		return nil, err
	}
	documentationRegistry, err := adaptDocumentationRegistry(documentationRuntime.Handlers.Registry())
	if err != nil {
		return nil, err
	}
	registry, err := completeRegistry(
		knowledgeBaseRegistry,
		providerRegistry,
		sourceRegistry,
		discordRegistry,
		retentionRegistry,
		documentationRegistry,
	)
	if err != nil {
		return nil, err
	}
	services := make([]service, 0, slotPool.Capacity()+2)
	for range slotPool.Capacity() {
		runner, runnerErr := worker.NewRunner(documentationRuntime.Queue, registry, config.runner, logger)
		if runnerErr != nil {
			return nil, runnerErr
		}
		services = append(services, runner.Run)
	}
	services = append(services,
		func(ctx context.Context) error {
			return sources.RunPolling(
				ctx, sourceStore, config.sources.PollScanEvery, config.sources.PollBatchSize,
				func(error) { logger.Error("source_poll_iteration_failed") },
			)
		},
		func(ctx context.Context) error {
			return retention.RunScheduling(
				ctx, retentionService, config.runtime.RetentionScanEvery,
				func(error) { logger.Error("retention_schedule_iteration_failed") },
			)
		},
	)
	return services, nil
}

func composeSources(
	pool *pgxpool.Pool,
	queue *jobs.Store,
	vault *security.CredentialVault,
	secrets *credentials.SecretReader,
	artifacts *sourcefiles.Store,
	config sources.RuntimeConfig,
) (*sources.Store, worker.Registry, error) {
	validator, repository, err := sources.NewRepositoryRuntime(artifacts, config)
	if err != nil {
		return nil, nil, err
	}
	websiteTransport, err := sources.NewPinnedHTTPSTransport(sources.PinnedHTTPSOptions{})
	if err != nil {
		return nil, nil, errors.New("configure website transport")
	}
	tinyFish, err := sources.NewTinyFishFetchClient(sources.NewTinyFishHTTPClientTransport(), 0)
	if err != nil {
		return nil, nil, errors.New("configure TinyFish transport")
	}
	website, err := sources.NewWebsiteSourceAdapter(artifacts, websiteTransport, tinyFish)
	if err != nil {
		return nil, nil, errors.New("configure website source runtime")
	}
	execution, err := sources.NewExecution(secrets, validator, repository, artifacts, website)
	if err != nil {
		return nil, nil, errors.New("configure source execution")
	}
	store, err := sources.NewStore(pool, queue, vault)
	if err != nil {
		return nil, nil, errors.New("configure source store")
	}
	registry, err := sources.Handlers(store, execution)
	if err != nil {
		return nil, nil, err
	}
	return store, registry, nil
}
