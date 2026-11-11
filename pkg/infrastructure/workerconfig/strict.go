package workerconfig

import (
	"os"
	"strconv"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

type envIntBound struct {
	key string
	min int
	max int
}

const (
	minSoloEngineBootTimeoutSec     = 5
	maxSoloEngineBootTimeoutSec     = 1800
	defaultSoloEngineBootTimeoutSec = 120
	minWorkerIdleThresholdSec       = 15
	maxWorkerIdleThresholdSec       = 3600
)

var workerIntEnvSpecs = []envIntBound{
	{key: "GPU_GRID_SOLO_BOOT_TIMEOUT_SEC", min: minSoloEngineBootTimeoutSec, max: maxSoloEngineBootTimeoutSec},
	{key: "SOLO_ENGINE_BOOT_TIMEOUT_SEC", min: minSoloEngineBootTimeoutSec, max: maxSoloEngineBootTimeoutSec},
	{key: "GPU_GRID_IDLE_SEC", min: minWorkerIdleThresholdSec, max: maxWorkerIdleThresholdSec},
}

var workerBoolEnvKeys = []string{
	"GPU_GRID_INSECURE_WS",
	"INSECURE_WS",
}

func ValidateWorkerEnv(lookup func(string) (string, bool)) error {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	for _, spec := range workerIntEnvSpecs {
		if err := validateIntLookup(lookup, spec); err != nil {
			return err
		}
	}
	for _, key := range workerBoolEnvKeys {
		if err := validateBoolLookup(lookup, key); err != nil {
			return err
		}
	}
	return validateWorkerEnumsAndBackendsLookup(lookup)
}

func validateWorkerEnumsAndBackendsLookup(lookup func(string) (string, bool)) error {
	if rawMode, ok := lookup("GPU_GRID_MODEL_MODE"); ok && strings.TrimSpace(rawMode) != "" {
		if _, err := nodecore.ParseServingRole(rawMode); err != nil {
			return err
		}
	}
	if rawSched, ok := lookup("GPU_GRID_SCHEDULE_MODE"); ok && strings.TrimSpace(rawSched) != "" {
		if _, err := nodecore.ParseIdleMode(rawSched); err != nil {
			return err
		}
	}
	if err := validateBackendsLookup(lookup, "GPU_GRID_BACKENDS"); err != nil {
		return err
	}
	return validateBackendsLookup(lookup, "ENABLED_BACKENDS")
}

func parseAndNormalizeBackends(rawList []string) ([]string, error) {
	if len(rawList) == 0 {
		return defaultEnabledBackends(), nil
	}
	seen := make(map[string]bool, len(rawList))
	out := make([]string, 0, len(rawList))
	for _, item := range rawList {
		parsed, err := nodecore.ParseEngineBackend(item)
		if err != nil {
			return nil, derr.Validationf(derr.CodeValidation, "invalid engine backend %q", item)
		}
		key := parsed.String()
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out, nil
}

func defaultEnabledBackends() []string {
	return []string{
		string(nodecore.BackendLlamaLocal),
		string(nodecore.BackendLlamaRPC),
		string(nodecore.BackendDiffusionLocal),
		string(nodecore.BackendWhisperLocal),
		string(nodecore.BackendAudioLocal),
		string(nodecore.BackendOmniLocal),
	}
}

func validateIntLookup(lookup func(string) (string, bool), spec envIntBound) error {
	raw, ok := lookup(spec.key)
	clean := strings.TrimSpace(raw)
	if !ok || clean == "" {
		return nil
	}
	v, err := strconv.Atoi(clean)
	if err != nil || v < spec.min || v > spec.max {
		return derr.Validationf(derr.CodeValidation, "environment variable %s=%q must be an integer between %d and %d", spec.key, raw, spec.min, spec.max)
	}
	return nil
}

func validateBoolLookup(lookup func(string) (string, bool), key string) error {
	raw, ok := lookup(key)
	clean := strings.ToLower(strings.TrimSpace(raw))
	if !ok || clean == "" {
		return nil
	}
	switch clean {
	case "1", "true", "yes", "on", "0", "false", "no", "off":
		return nil
	default:
		return derr.Validationf(derr.CodeValidation, "environment variable %s=%q must be a valid boolean (true/false/1/0/yes/no/on/off)", key, raw)
	}
}

func validateBackendsLookup(lookup func(string) (string, bool), key string) error {
	raw, ok := lookup(key)
	clean := strings.TrimSpace(raw)
	if !ok || clean == "" {
		return nil
	}
	_, err := parseAndNormalizeBackends(splitCSV(clean))
	return err
}
