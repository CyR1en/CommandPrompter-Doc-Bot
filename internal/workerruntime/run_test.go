package workerruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/cyr1en/ref0/internal/capsule"
)

func TestConfigurationAndCapsuleSlotsPreserveProductionContract(t *testing.T) {
	root := t.TempDir()
	paths := [2]string{
		filepath.Join(root, "capsule-0", "capsule.sock"),
		filepath.Join(root, "capsule-1", "capsule.sock"),
	}
	setWorkerEnvironment(t, paths)
	t.Setenv("WORKER_ID", "worker-test")
	t.Setenv("APP_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("APP_VERSION", "test-version")
	t.Setenv("SOURCE_POLL_SCAN_SECONDS", "2.5")
	t.Setenv("SOURCE_POLL_BATCH_SIZE", "7")

	config, err := configurationFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if config.runner.WorkerID != "worker-test" || config.runtime.ApplicationVersion != "test-version" {
		t.Fatalf("worker/version=%q/%q", config.runner.WorkerID, config.runtime.ApplicationVersion)
	}
	if config.sources.PollScanEvery.Seconds() != 2.5 || config.sources.PollBatchSize != 7 {
		t.Fatalf("source configuration=%#v", config.sources)
	}
	if config.vault.ActiveKeyID() != "active" {
		t.Fatalf("active key=%q", config.vault.ActiveKeyID())
	}
	slots, err := capsuleSlots(config.runtime.CapsuleSocketPaths)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 2 || slots[0].Name != "slot-0" || slots[1].Name != "slot-1" ||
		slots[0].SocketPath != paths[0] || slots[1].SocketPath != paths[1] {
		t.Fatalf("slots=%#v", slots)
	}
	pool, err := newCapsulePool(config.runtime.CapsuleSocketPaths)
	if err != nil {
		t.Fatal(err)
	}
	if pool.Capacity() != 2 || pool.State() != capsule.PoolNew {
		t.Fatalf("pool capacity/state=%d/%s", pool.Capacity(), pool.State())
	}
	if err = pool.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsCapsuleTopologyBeforeDatabaseConnection(t *testing.T) {
	root := t.TempDir()
	paths := [2]string{
		filepath.Join(root, "missing-0", "capsule.sock"),
		filepath.Join(root, "missing-1", "capsule.sock"),
	}
	setWorkerEnvironment(t, paths)
	t.Setenv("DATABASE_URL", "postgresql://ref0:secret@127.0.0.1:1/ref0?connect_timeout=1")
	err := Run(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || err.Error() != "capsule slot pool is unavailable" {
		t.Fatalf("error=%v", err)
	}
}

func setWorkerEnvironment(t *testing.T, paths [2]string) {
	t.Helper()
	key := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	t.Setenv("APP_MASTER_KEY", "active:"+key)
	t.Setenv("APP_PREVIOUS_MASTER_KEYS", "")
	t.Setenv("DATABASE_URL", "postgresql://ref0:secret@127.0.0.1:5432/ref0")
	t.Setenv("DOCUMENTATION_AGENT_RUNTIME", "pi-capsule")
	t.Setenv("PI_CAPSULE_SOCKET_PATHS", "[\""+paths[0]+"\",\""+paths[1]+"\"]")
}
