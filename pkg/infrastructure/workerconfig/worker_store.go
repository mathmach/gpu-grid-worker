package workerconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

type WorkerPersistedConfig = nodecore.WorkerPersistedConfig

type WorkerConfigFileStore struct {
	mu      sync.Mutex
	dataDir string
	path    string
}

func NewWorkerConfigStore(dataDir, cfgPath string) *WorkerConfigFileStore {
	resolvedDir := resolveWorkerDataDir(dataDir)
	if err := ensurePrivateDir(resolvedDir); err != nil {
		resolvedDir = filepath.Clean(resolvedDir)
	}
	resolvedPath := strings.TrimSpace(cfgPath)
	if resolvedPath == "" {
		resolvedPath = filepath.Join(resolvedDir, "config.json")
	}
	return &WorkerConfigFileStore{
		dataDir: resolvedDir,
		path:    resolvedPath,
	}
}

func resolveWorkerDataDir(configured string) string {
	if strings.TrimSpace(configured) != "" {
		return filepath.Clean(strings.TrimSpace(configured))
	}
	return filepath.Join(safeUserCacheBaseDir(), "gpu-grid-worker")
}

func safeUserCacheBaseDir() string {
	if dir, err := os.UserCacheDir(); err == nil && strings.TrimSpace(dir) != "" {
		return strings.TrimSpace(dir)
	}
	if dir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(dir) != "" {
		return strings.TrimSpace(dir)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(strings.TrimSpace(home), ".cache")
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		return filepath.Join(strings.TrimSpace(cwd), ".gpu-grid-cache")
	}
	return ".gpu-grid-cache"
}

func ensurePrivateDir(dir string) error {
	clean := filepath.Clean(strings.TrimSpace(dir))
	if clean == "" || clean == "." {
		return nil
	}
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(clean, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (s *WorkerConfigFileStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *WorkerConfigFileStore) DataDir() string {
	if s == nil {
		return ""
	}
	return s.dataDir
}

func (s *WorkerConfigFileStore) LoadConfig() (WorkerPersistedConfig, bool, error) {
	return s.Load()
}

func (s *WorkerConfigFileStore) SaveConfig(cfg WorkerPersistedConfig) error {
	return s.Save(cfg)
}

func (s *WorkerConfigFileStore) Load() (WorkerPersistedConfig, bool, error) {
	if s == nil || s.path == "" {
		return WorkerPersistedConfig{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WorkerPersistedConfig{}, false, nil
		}
		return WorkerPersistedConfig{}, false, err
	}
	var cfg WorkerPersistedConfig
	if err := decodeStrictWorkerConfig(raw, &cfg); err != nil {
		return WorkerPersistedConfig{}, false, derr.Wrap(derr.CodeValidation, 400, "corrupt worker config JSON", err)
	}
	return cfg, true, nil
}

func decodeStrictWorkerConfig(raw []byte, dst *WorkerPersistedConfig) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON data")
		}
		return err
	}
	return nil
}

func (s *WorkerConfigFileStore) Save(cfg WorkerPersistedConfig) error {
	if s == nil || s.path == "" {
		return derr.New(derr.CodeValidation, 400, "worker config path is required", nil)
	}

	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	parentDir := filepath.Dir(s.path)
	if err := ensurePrivateDir(parentDir); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(parentDir, ".worker-config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	if err := writeAndSyncTempFile(tmpFile, payload); err != nil {
		removeFileQuietly(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		removeFileQuietly(tmpPath)
		return err
	}
	return os.Chmod(s.path, 0o600)
}

func writeAndSyncTempFile(f *os.File, payload []byte) error {
	if err := f.Chmod(0o600); err != nil {
		closeConfigTempQuietly(f)
		return err
	}
	if _, err := f.Write(payload); err != nil {
		closeConfigTempQuietly(f)
		return err
	}
	if err := f.Sync(); err != nil {
		closeConfigTempQuietly(f)
		return err
	}
	return f.Close()
}

func closeConfigTempQuietly(f *os.File) {
	if f != nil {
		if err := f.Close(); err != nil {
			return
		}
	}
}

func removeFileQuietly(path string) {
	if path != "" {
		if err := os.Remove(path); err != nil {
			return
		}
	}
}
