package capsule

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const poolError = "capsule slot pool unavailable"

type PoolState string

const (
	PoolNew     PoolState = "NEW"
	PoolReady   PoolState = "READY"
	PoolClosing PoolState = "CLOSING"
	PoolClosed  PoolState = "CLOSED"
)

type Slot struct {
	Name       string
	SocketPath string
}

func NewSlot(name, socketPath string) (Slot, error) {
	clean := filepath.Clean(socketPath)
	if name == "" || name != strings.TrimSpace(name) || len([]byte(name)) > 128 || !filepath.IsAbs(clean) {
		return Slot{}, errors.New(poolError)
	}
	return Slot{Name: name, SocketPath: clean}, nil
}

type slotResult struct {
	slot Slot
	err  error
}

type slotWaiter struct {
	result chan slotResult
	slot   *Slot
}

type SlotPool struct {
	mu      sync.Mutex
	slots   []Slot
	state   PoolState
	free    []Slot
	leased  map[Slot]struct{}
	waiters []*slotWaiter
	closed  chan struct{}
}

func NewSlotPool(slots []Slot) (*SlotPool, error) {
	if len(slots) == 0 {
		return nil, errors.New(poolError)
	}
	names, paths := map[string]struct{}{}, map[string]struct{}{}
	copySlots := append([]Slot(nil), slots...)
	for index, slot := range copySlots {
		normalized, err := NewSlot(slot.Name, slot.SocketPath)
		if err != nil {
			return nil, err
		}
		path := normalizedPath(normalized.SocketPath)
		if _, exists := names[normalized.Name]; exists {
			return nil, errors.New(poolError)
		}
		if _, exists := paths[path]; exists {
			return nil, errors.New(poolError)
		}
		names[normalized.Name], paths[path] = struct{}{}, struct{}{}
		copySlots[index] = normalized
	}
	return &SlotPool{slots: copySlots, state: PoolNew, leased: map[Slot]struct{}{}, closed: make(chan struct{})}, nil
}

func (pool *SlotPool) State() PoolState {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.state
}

func (pool *SlotPool) Capacity() int { return len(pool.slots) }

func (pool *SlotPool) Start() error {
	return pool.start(validateTopology)
}

func (pool *SlotPool) start(validate func([]Slot) error) error {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.state != PoolNew {
		return errors.New(poolError)
	}
	if validate != nil {
		if err := validate(pool.slots); err != nil {
			return errors.New(poolError)
		}
	}
	pool.free = append(pool.free, pool.slots...)
	pool.state = PoolReady
	return nil
}

func (pool *SlotPool) Acquire(ctx context.Context) (Slot, error) {
	waiter := &slotWaiter{result: make(chan slotResult, 1)}
	pool.mu.Lock()
	if pool.state != PoolReady {
		pool.mu.Unlock()
		return Slot{}, errors.New(poolError)
	}
	pool.waiters = append(pool.waiters, waiter)
	pool.dispatchLocked()
	pool.mu.Unlock()

	select {
	case result := <-waiter.result:
		if result.err != nil {
			return Slot{}, result.err
		}
		pool.mu.Lock()
		if pool.state != PoolReady {
			if _, leased := pool.leased[result.slot]; leased {
				pool.releaseLocked(result.slot)
			}
			pool.mu.Unlock()
			return Slot{}, errors.New(poolError)
		}
		pool.mu.Unlock()
		return result.slot, result.err
	case <-ctx.Done():
		pool.mu.Lock()
		if waiter.slot != nil {
			pool.releaseLocked(*waiter.slot)
		} else {
			for index, queued := range pool.waiters {
				if queued == waiter {
					pool.waiters = append(pool.waiters[:index], pool.waiters[index+1:]...)
					break
				}
			}
			pool.dispatchLocked()
		}
		pool.mu.Unlock()
		return Slot{}, ctx.Err()
	}
}

func (pool *SlotPool) Release(slot Slot) error {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if _, exists := pool.leased[slot]; !exists {
		return errors.New(poolError)
	}
	pool.releaseLocked(slot)
	return nil
}

func (pool *SlotPool) Close(ctx context.Context) error {
	pool.mu.Lock()
	switch pool.state {
	case PoolClosed:
		pool.mu.Unlock()
		return nil
	case PoolNew:
		pool.state = PoolClosed
		close(pool.closed)
		pool.mu.Unlock()
		return nil
	case PoolReady:
		pool.state = PoolClosing
		for _, waiter := range pool.waiters {
			waiter.result <- slotResult{err: errors.New(poolError)}
		}
		pool.waiters = nil
		if len(pool.leased) == 0 {
			pool.state = PoolClosed
			close(pool.closed)
		}
	}
	closed := pool.closed
	pool.mu.Unlock()
	select {
	case <-closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (pool *SlotPool) dispatchLocked() {
	for pool.state == PoolReady && len(pool.free) != 0 && len(pool.waiters) != 0 {
		waiter := pool.waiters[0]
		pool.waiters = pool.waiters[1:]
		slot := pool.free[0]
		pool.free = pool.free[1:]
		pool.leased[slot] = struct{}{}
		waiter.slot = &slot
		waiter.result <- slotResult{slot: slot}
	}
}

func (pool *SlotPool) releaseLocked(slot Slot) {
	delete(pool.leased, slot)
	if pool.state == PoolReady {
		pool.free = append(pool.free, slot)
		pool.dispatchLocked()
	} else if pool.state == PoolClosing && len(pool.leased) == 0 {
		pool.state = PoolClosed
		close(pool.closed)
	}
}

func validateTopology(slots []Slot) error {
	paths := map[string]struct{}{}
	parents := map[fileIdentity]struct{}{}
	for _, slot := range slots {
		path := filepath.Clean(slot.SocketPath)
		normalized := normalizedPath(path)
		if filepath.Base(path) != "capsule.sock" {
			return errors.New(poolError)
		}
		if _, exists := paths[normalized]; exists {
			return errors.New(poolError)
		}
		paths[normalized] = struct{}{}
		if err := rejectSymlinkComponents(path); err != nil {
			return err
		}
		parentInfo, err := os.Lstat(filepath.Dir(path))
		if err != nil {
			return err
		}
		socketInfo, err := os.Lstat(path)
		if err != nil {
			return err
		}
		parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
		if !ok || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() ||
			uint32(parentStat.Uid) != 0 || uint32(parentStat.Gid) != 2_000 || uint32(parentStat.Mode)&0o7777 != 0o2750 {
			return errors.New(poolError)
		}
		socketStat, ok := socketInfo.Sys().(*syscall.Stat_t)
		if !ok || socketInfo.Mode()&os.ModeSymlink != 0 || socketInfo.Mode()&os.ModeSocket == 0 ||
			uint32(socketStat.Uid) != 0 || uint32(socketStat.Gid) != 2_000 || uint32(socketStat.Mode)&0o7777 != 0o660 {
			return errors.New(poolError)
		}
		identity := fileIdentity{device: uint64(parentStat.Dev), inode: uint64(parentStat.Ino)}
		if _, exists := parents[identity]; exists {
			return errors.New(poolError)
		}
		parents[identity] = struct{}{}
		if unix.Faccessat(unix.AT_FDCWD, filepath.Dir(path), unix.X_OK, unix.AT_EACCESS) != nil ||
			unix.Faccessat(unix.AT_FDCWD, path, unix.R_OK|unix.W_OK, unix.AT_EACCESS) != nil {
			return errors.New(poolError)
		}
	}
	return nil
}

type fileIdentity struct{ device, inode uint64 }

func rejectSymlinkComponents(path string) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return errors.New(poolError)
	}
	volume := filepath.VolumeName(clean)
	current := string(filepath.Separator)
	if volume != "" {
		current = volume + string(filepath.Separator)
	}
	relative := strings.TrimPrefix(strings.TrimPrefix(clean, volume), string(filepath.Separator))
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New(poolError)
		}
	}
	return nil
}

func normalizedPath(path string) string {
	clean := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(clean)
	}
	return clean
}

func (slot Slot) String() string { return fmt.Sprintf("CapsuleSlot(%q)", slot.Name) }
