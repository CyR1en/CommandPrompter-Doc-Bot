package workerruntime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunServicesPropagatesCancellationToEveryService(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 3)
	var stopped atomic.Int32
	services := make([]service, 3)
	for index := range services {
		services[index] = func(ctx context.Context) error {
			started <- struct{}{}
			<-ctx.Done()
			stopped.Add(1)
			return nil
		}
	}
	finished := make(chan error, 1)
	go func() { finished <- runServices(ctx, services...) }()
	for range services {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("service did not start")
		}
	}
	cancel()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if stopped.Load() != int32(len(services)) {
		t.Fatalf("stopped=%d", stopped.Load())
	}
}

func TestRunServicesPropagatesFailureAndStopsPeers(t *testing.T) {
	expected := errors.New("runner failed")
	var stopped atomic.Int32
	peer := func(ctx context.Context) error {
		<-ctx.Done()
		stopped.Add(1)
		return nil
	}
	if err := runServices(context.Background(), peer, func(context.Context) error {
		return expected
	}, peer); !errors.Is(err, expected) {
		t.Fatalf("error=%v", err)
	}
	if stopped.Load() != 2 {
		t.Fatalf("stopped peers=%d", stopped.Load())
	}
}

func TestRunServicesRejectsIncompleteAndPrematureServices(t *testing.T) {
	if err := runServices(context.Background()); err == nil {
		t.Fatal("empty services were accepted")
	}
	if err := runServices(context.Background(), nil); err == nil {
		t.Fatal("nil service was accepted")
	}
	if err := runServices(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, errServiceStopped) {
		t.Fatalf("premature service error=%v", err)
	}
}
