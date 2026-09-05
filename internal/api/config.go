package api

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/database"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultAPIPort        = 8000
	defaultDataDir        = "/app/data"
	defaultFrontendDir    = "frontend/dist"
	defaultVersion        = "0.1.0"
	defaultSessionTTL     = 10_080 * time.Minute
	defaultBootstrapTTL   = 30 * time.Minute
	defaultDeleteGrace    = 72 * time.Hour
	defaultEventPoll      = time.Second
	defaultEventBeat      = 15 * time.Second
	minMetricsTokenLength = 32
	maxMetricsTokenLength = 512
	databaseTimeout       = 5 * time.Second
	shutdownGracePeriod   = 10 * time.Second
)

// Config contains validated API runtime settings. Secret-bearing fields stay
// private so logging a Config cannot disclose credentials.
type Config struct {
	address             string
	dataDir             string
	frontendDir         string
	version             string
	poolConfig          *pgxpool.Config
	vault               *security.CredentialVault
	bootstrapToken      *security.SecretValue
	bootstrapTokenTTL   time.Duration
	deleteGrace         time.Duration
	sessionTTL          time.Duration
	sessionCookieMaxAge int
	sessionCookieSecure bool
	eventPollInterval   time.Duration
	eventBeatInterval   time.Duration
	eventLimit          int
	eventBeatLimit      int
	metricsBearerToken  *security.SecretValue
	metricsReader       metricsReader
	applicationMetrics  *applicationMetrics
}

// ConfigFromEnvironment preserves the existing APP_*, POSTGRES_*, DATABASE_URL,
// and API_PORT deployment contract.
func ConfigFromEnvironment() (Config, error) {
	databaseURL, err := database.URLFromEnvironment()
	if err != nil {
		return Config{}, err
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return Config{}, errors.New("database configuration is invalid")
	}
	if poolConfig.ConnConfig.ConnectTimeout == 0 {
		poolConfig.ConnConfig.ConnectTimeout = databaseTimeout
	}

	vault, err := security.NewCredentialVault(
		os.Getenv("APP_MASTER_KEY"),
		os.Getenv("APP_PREVIOUS_MASTER_KEYS"),
	)
	if err != nil {
		return Config{}, err
	}

	var bootstrapToken *security.SecretValue
	if value := os.Getenv("APP_BOOTSTRAP_TOKEN"); value != "" {
		bootstrapToken, err = security.NewSecretValue(value)
		if err != nil {
			return Config{}, errors.New("APP_BOOTSTRAP_TOKEN is invalid")
		}
	}
	sessionTTL, err := durationMinutes("OPERATOR_SESSION_TTL_MINUTES", defaultSessionTTL)
	if err != nil {
		return Config{}, err
	}
	bootstrapTTL, err := durationMinutes("BOOTSTRAP_TOKEN_TTL_MINUTES", defaultBootstrapTTL)
	if err != nil {
		return Config{}, err
	}
	deleteGrace, err := durationHours("KNOWLEDGE_BASE_DELETE_GRACE_HOURS", defaultDeleteGrace)
	if err != nil {
		return Config{}, err
	}
	origin, err := url.Parse(environmentOr("PUBLIC_ORIGIN", "http://localhost:8000"))
	if err != nil || origin.Host == "" || (origin.Scheme != "http" && origin.Scheme != "https") {
		return Config{}, errors.New("PUBLIC_ORIGIN must be an absolute HTTP URL")
	}
	if origin.Scheme == "http" && !isLoopbackHostname(origin.Hostname()) {
		return Config{}, errors.New("PUBLIC_ORIGIN must use HTTPS except for loopback development")
	}
	metricsBearerToken, err := metricsSecret(os.Getenv("METRICS_BEARER_TOKEN"))
	if err != nil {
		return Config{}, err
	}
	cookieSeconds := sessionTTL / time.Second
	maxInt := int64(^uint(0) >> 1)
	if int64(cookieSeconds) > maxInt {
		return Config{}, errors.New("OPERATOR_SESSION_TTL_MINUTES is too large")
	}

	version := environmentOr("APP_VERSION", defaultVersion)
	if utf8.RuneCountInString(version) > 64 || strings.IndexFunc(version, unicode.IsSpace) >= 0 || strings.ContainsRune(version, '\x00') {
		return Config{}, errors.New("APP_VERSION must be a non-empty token of at most 64 characters")
	}

	port := defaultAPIPort
	if value := os.Getenv("API_PORT"); value != "" {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || parsed < 1 || parsed > 65535 {
			return Config{}, errors.New("API_PORT must be between 1 and 65535")
		}
		port = parsed
	}
	host := environmentOr("API_HOST", "0.0.0.0")
	if strings.ContainsAny(host, "\x00\r\n\t /\\") {
		return Config{}, errors.New("API_HOST is invalid")
	}

	dataDir := environmentOr("APP_DATA_DIR", defaultDataDir)
	frontendDir := environmentOr("APP_FRONTEND_DIR", defaultFrontendDir)
	return Config{
		address:             net.JoinHostPort(host, strconv.Itoa(port)),
		dataDir:             dataDir,
		frontendDir:         frontendDir,
		version:             version,
		poolConfig:          poolConfig,
		vault:               vault,
		bootstrapToken:      bootstrapToken,
		bootstrapTokenTTL:   bootstrapTTL,
		deleteGrace:         deleteGrace,
		sessionTTL:          sessionTTL,
		sessionCookieMaxAge: int(cookieSeconds),
		sessionCookieSecure: origin.Scheme == "https",
		eventPollInterval:   defaultEventPoll,
		eventBeatInterval:   defaultEventBeat,
		metricsBearerToken:  metricsBearerToken,
	}, nil
}

func isLoopbackHostname(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func metricsSecret(value string) (*security.SecretValue, error) {
	if len(value) < minMetricsTokenLength || len(value) > maxMetricsTokenLength {
		return nil, errors.New("METRICS_BEARER_TOKEN must contain 32 to 512 ASCII token characters")
	}
	for _, character := range value {
		if !metricsTokenCharacter(character) {
			return nil, errors.New("METRICS_BEARER_TOKEN must contain 32 to 512 ASCII token characters")
		}
	}
	secret, err := security.NewSecretValue(value)
	if err != nil {
		return nil, errors.New("METRICS_BEARER_TOKEN is invalid")
	}
	return secret, nil
}

func metricsTokenCharacter(character rune) bool {
	return character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' ||
		strings.ContainsRune("-._~+/=", character)
}

func durationHours(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	hours, err := strconv.ParseInt(value, 10, 64)
	if err != nil || hours <= 0 || hours > int64((1<<63-1)/time.Hour) {
		return 0, errors.New(name + " must be a positive number of hours")
	}
	return time.Duration(hours) * time.Hour, nil
}

func (config Config) eventSettings() (eventStreamSettings, error) {
	settings := eventStreamSettings{
		pollInterval: config.eventPollInterval,
		beatInterval: config.eventBeatInterval,
		eventLimit:   config.eventLimit,
		beatLimit:    config.eventBeatLimit,
	}
	if settings.pollInterval == 0 {
		settings.pollInterval = defaultEventPoll
	}
	if settings.beatInterval == 0 {
		settings.beatInterval = defaultEventBeat
	}
	if settings.pollInterval < time.Nanosecond || settings.pollInterval > defaultEventPoll {
		return eventStreamSettings{}, errors.New("event stream poll interval must be at most one second")
	}
	if settings.beatInterval < time.Nanosecond || settings.beatInterval > defaultEventBeat {
		return eventStreamSettings{}, errors.New("event stream heartbeat interval must be at most 15 seconds")
	}
	if settings.eventLimit < 0 || settings.beatLimit < 0 {
		return eventStreamSettings{}, errors.New("event stream limits must be positive")
	}
	return settings, nil
}

func durationMinutes(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	minutes, err := strconv.ParseInt(value, 10, 64)
	if err != nil || minutes <= 0 || minutes > int64((1<<63-1)/time.Minute) {
		return 0, errors.New(name + " must be a positive number of minutes")
	}
	return time.Duration(minutes) * time.Minute, nil
}

func environmentOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
