package worker_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
)

func TestCorruptPersistedConfigSurfacesExplicitLastErrorWarning(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte("{corrupt-json"), 0o600); err != nil {
		t.Fatalf("write corrupt config.json: %v", err)
	}

	eng := worker.NewEngine(baseTestDeps(dir, cfgPath))
	snap := eng.Snapshot()
	if !strings.Contains(snap.LastError, "invalid worker config.json:") {
		t.Fatalf("expected corrupt config.json warning in LastError, got %q", snap.LastError)
	}
	if snap.ModelMode != "auto" || snap.UIMode != "simple" {
		t.Fatalf("expected unconfigured worker defaults (ModelMode=auto, UIMode=simple), got mode=%q ui=%q", snap.ModelMode, snap.UIMode)
	}

	legacyDir := t.TempDir()
	legacyCfgPath := filepath.Join(legacyDir, "config.json")
	if err := os.WriteFile(legacyCfgPath, []byte(`{"askPer1M":0.45}`), 0o600); err != nil {
		t.Fatalf("write legacy askPer1M config.json: %v", err)
	}
	legacyEng := worker.NewEngine(baseTestDeps(legacyDir, legacyCfgPath))
	if !strings.Contains(legacyEng.Snapshot().LastError, "invalid worker config.json:") {
		t.Fatalf("expected removed askPer1M field in config.json to surface LastError warning, got %q", legacyEng.Snapshot().LastError)
	}
}
