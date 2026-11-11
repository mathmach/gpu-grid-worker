package workerconfig_test

import (
	"path/filepath"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/workerconfig"
)

func TestWorkerConfigFileStoreAtomicRoundTrip(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "worker", "config.json")
	store := workerconfig.NewWorkerConfigStore(filepath.Dir(storePath), storePath)

	if _, found, err := store.Load(); err != nil || found {
		t.Fatalf("expected missing worker config to return found=false err=nil, got found=%v err=%v", found, err)
	}

	in := workerconfig.WorkerPersistedConfig{
		GatewayURL:       "https://grid.example.com",
		WorkerToken:      "wk_test_123",
		WorkerName:       "rig-alpha",
		SelectedIDs:      []string{"Vulkan0"},
		ScheduleMode:     "idle",
		IdleThresholdSec: 120,
		Language:         "pt-BR",
	}
	if err := store.Save(in); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	loaded, found, err := store.Load()
	if err != nil || !found {
		t.Fatalf("Load failed: found=%v err=%v", found, err)
	}
	if loaded.GatewayURL != in.GatewayURL || loaded.WorkerName != in.WorkerName || loaded.ScheduleMode != "idle" || loaded.IdleThresholdSec != 120 || loaded.Language != "pt-BR" {
		t.Fatalf("unexpected loaded worker config: %+v", loaded)
	}
}
