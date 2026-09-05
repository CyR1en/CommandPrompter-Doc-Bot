package capsule

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func testSlots(t *testing.T) []Slot {
	t.Helper()
	one, err := NewSlot("one", "/run/capsule-one/capsule.sock")
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewSlot("two", "/run/capsule-two/capsule.sock")
	if err != nil {
		t.Fatal(err)
	}
	return []Slot{one, two}
}

func waitForWaiters(t *testing.T, pool *SlotPool, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		pool.mu.Lock()
		current := len(pool.waiters)
		pool.mu.Unlock()
		if current == count {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("waiter count did not reach %d", count)
}

func TestSlotPoolIsFIFOAndReturnsSlotsRoundRobin(t *testing.T) {
	pool, err := NewSlotPool(testSlots(t))
	if err != nil || pool.start(nil) != nil {
		t.Fatal(err)
	}
	first, _ := pool.Acquire(context.Background())
	second, _ := pool.Acquire(context.Background())
	a, b := make(chan Slot, 1), make(chan Slot, 1)
	go func() { slot, _ := pool.Acquire(context.Background()); a <- slot }()
	waitForWaiters(t, pool, 1)
	go func() { slot, _ := pool.Acquire(context.Background()); b <- slot }()
	waitForWaiters(t, pool, 2)
	if err := pool.Release(first); err != nil {
		t.Fatal(err)
	}
	if got := <-a; got != first {
		t.Fatalf("first FIFO waiter received %#v", got)
	} else if err := pool.Release(got); err != nil {
		t.Fatal(err)
	}
	if err := pool.Release(second); err != nil {
		t.Fatal(err)
	}
	if got := <-b; got != first {
		// The first waiter released its slot before the second original slot;
		// round-robin therefore makes that released slot next.
		t.Fatalf("second FIFO waiter received %#v", got)
	} else if err := pool.Release(got); err != nil {
		t.Fatal(err)
	}
	next, _ := pool.Acquire(context.Background())
	if next != second {
		t.Fatalf("round-robin order was not retained: %#v", next)
	}
	_ = pool.Release(next)
}

func TestSlotPoolCancellationAndCloseAreFailClosed(t *testing.T) {
	pool, _ := NewSlotPool(testSlots(t)[:1])
	_ = pool.start(nil)
	leased, _ := pool.Acquire(context.Background())
	waitCtx, cancelWait := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() { _, err := pool.Acquire(waitCtx); waited <- err }()
	waitForWaiters(t, pool, 1)
	cancelWait()
	if !errors.Is(<-waited, context.Canceled) {
		t.Fatal("cancelled acquire did not return context cancellation")
	}
	closed := make(chan error, 1)
	go func() { closed <- pool.Close(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for pool.State() != PoolClosing && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if pool.State() != PoolClosing {
		t.Fatal("pool did not enter closing state")
	}
	if _, err := pool.Acquire(context.Background()); err == nil {
		t.Fatal("closing pool accepted work")
	}
	_ = pool.Release(leased)
	if err := <-closed; err != nil || pool.State() != PoolClosed {
		t.Fatalf("pool did not close cleanly: %v %s", err, pool.State())
	}
}

func TestSlotTopologyRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(realDirectory, linked); err != nil {
		t.Fatal(err)
	}
	slot, _ := NewSlot("linked", filepath.Join(linked, "capsule.sock"))
	if err := validateTopology([]Slot{slot}); err == nil {
		t.Fatal("symlinked topology was accepted")
	}
}

func TestSlotTopologyAcceptsExactLinuxOwnershipAndModes(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("exact root-owned production topology requires root on Linux")
	}
	root := t.TempDir()
	slots := make([]Slot, 0, 2)
	listeners := make([]net.Listener, 0, 2)
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for index := 0; index < 2; index++ {
		directory := filepath.Join(root, string(rune('a'+index)))
		if err := os.Mkdir(directory, 0o750); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "capsule.sock")
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		if err := os.Chown(directory, 0, 2_000); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o750|os.ModeSetgid); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, 0, 2_000); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o660); err != nil {
			t.Fatal(err)
		}
		slot, _ := NewSlot(string(rune('a'+index)), path)
		slots = append(slots, slot)
	}
	if err := validateTopology(slots); err != nil {
		t.Fatal(err)
	}
}
