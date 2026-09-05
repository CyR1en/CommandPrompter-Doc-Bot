package worker

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/jobs"
)

const (
	defaultWorkerID      = "worker-1"
	defaultLeaseSeconds  = 60
	defaultPollSeconds   = 1
	defaultRetrySeconds  = 5
	maximumLeaseSeconds  = 3600
	maximumPollSeconds   = 60
	maximumRetrySeconds  = 3600
	maximumWorkerIDChars = 255
)

type Config struct {
	WorkerID       jobs.WorkerID
	LeaseFor       time.Duration
	HeartbeatEvery time.Duration
	PollEvery      time.Duration
	RetryBackoff   time.Duration
}

func ConfigFromEnvironment() (Config, error) {
	workerID, present := os.LookupEnv("WORKER_ID")
	if !present {
		workerID = defaultWorkerID
	}
	if workerID == "" || utf8.RuneCountInString(workerID) > maximumWorkerIDChars {
		return Config{}, errors.New("WORKER_ID must contain between 1 and 255 characters")
	}

	leaseSeconds, err := integerSeconds("JOB_LEASE_SECONDS", defaultLeaseSeconds, maximumLeaseSeconds)
	if err != nil {
		return Config{}, err
	}
	pollEvery, err := decimalSeconds("JOB_POLL_INTERVAL_SECONDS", defaultPollSeconds, maximumPollSeconds)
	if err != nil {
		return Config{}, err
	}
	retryBackoff, err := decimalSeconds("JOB_RETRY_BACKOFF_SECONDS", defaultRetrySeconds, maximumRetrySeconds)
	if err != nil {
		return Config{}, err
	}
	leaseFor := time.Duration(leaseSeconds) * time.Second
	config := Config{
		WorkerID:       jobs.WorkerID(workerID),
		LeaseFor:       leaseFor,
		HeartbeatEvery: leaseFor / 3,
		PollEvery:      pollEvery,
		RetryBackoff:   retryBackoff,
	}
	if err := config.validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) validate() error {
	if config.WorkerID == "" {
		return errors.New("worker ID must not be empty")
	}
	if utf8.RuneCountInString(string(config.WorkerID)) > maximumWorkerIDChars {
		return errors.New("worker ID must contain at most 255 characters")
	}
	if min(config.LeaseFor, config.HeartbeatEvery, config.PollEvery, config.RetryBackoff) <= 0 {
		return errors.New("runner durations must be positive")
	}
	if config.HeartbeatEvery >= config.LeaseFor {
		return errors.New("heartbeat must occur before lease expiry")
	}
	return nil
}

func integerSeconds(name string, fallback, maximum int64) (int64, error) {
	value, present := os.LookupEnv(name)
	if !present {
		return fallback, nil
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 || seconds > maximum {
		return 0, fmt.Errorf("%s must be an integer between 1 and %d", name, maximum)
	}
	return seconds, nil
}

func decimalSeconds(name string, fallback, maximum float64) (time.Duration, error) {
	value, present := os.LookupEnv(name)
	if !present {
		return time.Duration(fallback * float64(time.Second)), nil
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > maximum {
		return 0, fmt.Errorf("%s must be greater than 0 and at most %g", name, maximum)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
