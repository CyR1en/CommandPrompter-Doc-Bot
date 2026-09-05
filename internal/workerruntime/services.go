package workerruntime

import (
	"context"
	"errors"
)

var errServiceStopped = errors.New("worker service stopped unexpectedly")

func runServices(ctx context.Context, services ...service) error {
	if len(services) == 0 {
		return errors.New("worker services are incomplete")
	}
	for _, run := range services {
		if run == nil {
			return errors.New("worker services are incomplete")
		}
	}

	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, len(services))
	for _, run := range services {
		serviceRun := run
		go func() { results <- serviceRun(serviceCtx) }()
	}

	first := <-results
	cancel()
	for range len(services) - 1 {
		<-results
	}
	if ctx.Err() != nil {
		return nil
	}
	if first != nil {
		return first
	}
	return errServiceStopped
}
