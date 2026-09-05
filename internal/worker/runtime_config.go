package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	defaultWorkerDataDir      = "/app/data"
	defaultApplicationVersion = "0.1.0"
	defaultDeleteGraceHours   = 72
	defaultRetentionScan      = 3600 * time.Second
)

type RuntimeConfig struct {
	DataDir            string
	ApplicationVersion string
	DeleteGrace        time.Duration
	RetentionPolicy    RetentionConfig
	RetentionScanEvery time.Duration
	CapsuleSocketPaths [2]string
}

type RetentionConfig struct {
	SourceSnapshots time.Duration
	FailedDrafts    time.Duration
	JobLogs         time.Duration
	EventLog        time.Duration
	AgentRuns       time.Duration
	DiscordContext  time.Duration
	OldWikis        time.Duration
	BatchSize       int
}

func RuntimeConfigFromEnvironment() (RuntimeConfig, error) {
	version := environmentValue("APP_VERSION", defaultApplicationVersion)
	if version == "" || utf8.RuneCountInString(version) > 64 ||
		strings.IndexFunc(version, unicode.IsSpace) >= 0 || strings.ContainsRune(version, '\x00') {
		return RuntimeConfig{}, errors.New("APP_VERSION must be a non-empty token of at most 64 characters")
	}
	dataDir := filepath.Clean(environmentValue("APP_DATA_DIR", defaultWorkerDataDir))
	if !filepath.IsAbs(dataDir) {
		return RuntimeConfig{}, errors.New("APP_DATA_DIR must be absolute")
	}
	maximumHours := int((uint64(1)<<63 - 1) / uint64(time.Hour))
	deleteGrace, err := boundedDaysOrHours("KNOWLEDGE_BASE_DELETE_GRACE_HOURS", defaultDeleteGraceHours, maximumHours, time.Hour)
	if err != nil {
		return RuntimeConfig{}, err
	}
	retentionScan, err := boundedDecimalDuration("RETENTION_SCAN_SECONDS", defaultRetentionScan, 86_400*time.Second)
	if err != nil {
		return RuntimeConfig{}, err
	}
	batchSize, err := boundedInteger("RETENTION_BATCH_SIZE", 100, 1_000)
	if err != nil {
		return RuntimeConfig{}, err
	}
	sourceSnapshots, err := boundedDaysOrHours("SOURCE_SNAPSHOT_RETENTION_DAYS", 30, 3_650, 24*time.Hour)
	if err != nil {
		return RuntimeConfig{}, err
	}
	failedDrafts, err := boundedDaysOrHours("FAILED_DRAFT_RETENTION_DAYS", 14, 3_650, 24*time.Hour)
	if err != nil {
		return RuntimeConfig{}, err
	}
	jobLogs, err := boundedDaysOrHours("JOB_LOG_RETENTION_DAYS", 30, 3_650, 24*time.Hour)
	if err != nil {
		return RuntimeConfig{}, err
	}
	eventLog, err := boundedDaysOrHours("EVENT_LOG_RETENTION_DAYS", 30, 3_650, 24*time.Hour)
	if err != nil {
		return RuntimeConfig{}, err
	}
	agentRuns, err := boundedDaysOrHours("AGENT_RUN_RETENTION_DAYS", 90, 3_650, 24*time.Hour)
	if err != nil {
		return RuntimeConfig{}, err
	}
	discordContext, err := boundedDaysOrHours("DISCORD_CONTEXT_RETENTION_DAYS", 7, 3_650, 24*time.Hour)
	if err != nil {
		return RuntimeConfig{}, err
	}
	oldWikis, err := boundedDaysOrHours("OLD_WIKI_RETENTION_DAYS", 90, 3_650, 24*time.Hour)
	if err != nil {
		return RuntimeConfig{}, err
	}
	paths, err := capsulePathsFromEnvironment()
	if err != nil {
		return RuntimeConfig{}, err
	}
	config := RuntimeConfig{
		DataDir: dataDir, ApplicationVersion: version, DeleteGrace: deleteGrace,
		RetentionPolicy: RetentionConfig{
			SourceSnapshots: sourceSnapshots, FailedDrafts: failedDrafts,
			JobLogs: jobLogs, EventLog: eventLog, AgentRuns: agentRuns,
			DiscordContext: discordContext, OldWikis: oldWikis,
			BatchSize: batchSize,
		},
		RetentionScanEvery: retentionScan, CapsuleSocketPaths: paths,
	}
	return config, nil
}

func capsulePathsFromEnvironment() ([2]string, error) {
	var result [2]string
	if os.Getenv("DOCUMENTATION_AGENT_RUNTIME") != "pi-capsule" {
		return result, errors.New("worker documentation runtime is unavailable")
	}
	raw := os.Getenv("PI_CAPSULE_SOCKET_PATHS")
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	var values []string
	if raw == "" || decoder.Decode(&values) != nil {
		return result, errors.New("PI_CAPSULE_SOCKET_PATHS must be a JSON array of two socket paths")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) || len(values) != len(result) {
		return result, errors.New("PI_CAPSULE_SOCKET_PATHS must be a JSON array of two socket paths")
	}
	seen := map[string]struct{}{}
	for index, value := range values {
		path := filepath.Clean(value)
		if !filepath.IsAbs(path) || filepath.Base(path) != "capsule.sock" {
			return result, errors.New("capsule socket configuration is invalid")
		}
		normalized := path
		if _, duplicate := seen[normalized]; duplicate {
			return result, errors.New("capsule socket configuration is invalid")
		}
		seen[normalized] = struct{}{}
		result[index] = path
	}
	return result, nil
}

func boundedInteger(name string, fallback, maximum int) (int, error) {
	value := fallback
	if raw := os.Getenv(name); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return 0, errors.New(name + " must be an integer")
		}
		value = parsed
	}
	if value < 1 || value > maximum {
		return 0, errors.New(name + " is outside its supported range")
	}
	return value, nil
}

func boundedDaysOrHours(name string, fallback, maximum int, unit time.Duration) (time.Duration, error) {
	value, err := boundedInteger(name, fallback, maximum)
	if err != nil {
		return 0, err
	}
	return time.Duration(value) * unit, nil
}

func boundedDecimalDuration(name string, fallback, maximum time.Duration) (time.Duration, error) {
	if raw := os.Getenv(name); raw != "" {
		seconds, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > maximum.Seconds() {
			return 0, errors.New(name + " is outside its supported range")
		}
		return time.Duration(seconds * float64(time.Second)), nil
	}
	return fallback, nil
}

func environmentValue(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
