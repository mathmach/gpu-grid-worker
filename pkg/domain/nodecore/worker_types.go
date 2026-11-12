package nodecore

import (
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/fsm"
)

type WorkerSessionState string

const (
	WorkerConnectedUnauth WorkerSessionState = "CONNECTED_UNAUTH"
	WorkerIdle            WorkerSessionState = "IDLE"
	WorkerInPool          WorkerSessionState = "IN_POOL"
	WorkerPauseDraining   WorkerSessionState = "PAUSE_DRAINING"
	WorkerPaused          WorkerSessionState = "PAUSED"
	WorkerQuarantined     WorkerSessionState = "QUARANTINED"
	WorkerClosed          WorkerSessionState = "CLOSED"
)

var workerSessionFSM = fsm.NewTable("worker_session", true, map[WorkerSessionState][]WorkerSessionState{
	"":                    {WorkerConnectedUnauth, WorkerClosed},
	WorkerConnectedUnauth: {WorkerIdle, WorkerPaused, WorkerQuarantined, WorkerClosed},
	WorkerIdle:            {WorkerInPool, WorkerPauseDraining, WorkerPaused, WorkerQuarantined, WorkerClosed},
	WorkerInPool:          {WorkerIdle, WorkerPauseDraining, WorkerPaused, WorkerQuarantined, WorkerClosed},
	WorkerPauseDraining:   {WorkerPaused, WorkerInPool, WorkerIdle, WorkerQuarantined, WorkerClosed},
	WorkerPaused:          {WorkerIdle, WorkerInPool, WorkerPauseDraining, WorkerQuarantined, WorkerClosed},
	WorkerQuarantined:     {WorkerIdle, WorkerInPool, WorkerPaused, WorkerClosed},
	WorkerClosed:          {WorkerConnectedUnauth},
})

func (s WorkerSessionState) CanTransitionTo(next WorkerSessionState) bool {
	return workerSessionFSM.CanTransition(s, next)
}

func (s WorkerSessionState) TransitionTo(next WorkerSessionState) (WorkerSessionState, error) {
	return workerSessionFSM.Transition(s, next)
}

type WorkerScheduleMode string

const (
	ScheduleAlways WorkerScheduleMode = "always"
	ScheduleIdle   WorkerScheduleMode = "idle"
)

func ParseIdleMode(raw string) (WorkerScheduleMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ScheduleAlways):
		return ScheduleAlways, nil
	case string(ScheduleIdle):
		return ScheduleIdle, nil
	default:
		return "", derr.New(derr.CodeValidation, 400, "unsupported worker schedule mode; expected always or idle", map[string]any{"scheduleMode": raw})
	}
}

type ModelSelectionMode string

const (
	ModelModeAuto  ModelSelectionMode = "auto"
	ModelModeMulti ModelSelectionMode = "multi"
)

func NormalizeModelID(raw string) string {
	clean := strings.ToLower(strings.TrimSpace(raw))
	return StripModelExtension(clean)
}

func ParseServingRole(raw string) (ModelSelectionMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ModelModeAuto):
		return ModelModeAuto, nil
	case string(ModelModeMulti):
		return ModelModeMulti, nil
	default:
		return "", derr.New(derr.CodeValidation, 400, "unsupported worker serving role; expected auto or multi", map[string]any{"modelMode": raw})
	}
}

func ParseModelSelection(rawMode string, rawModels []string) (ModelSelectionMode, []string, error) {
	role, err := ParseServingRole(rawMode)
	if err != nil {
		return "", nil, err
	}
	cleaned := deduplicateNormalizedModels(rawModels)
	if role == ModelModeMulti {
		if len(cleaned) == 0 {
			return "", nil, derr.New(derr.CodeValidation, 400, "multi serving role requires at least one selected model", nil)
		}
		return ModelModeMulti, cleaned, nil
	}
	return ModelModeAuto, nil, nil
}

func deduplicateNormalizedModels(rawModels []string) []string {
	seen := make(map[string]bool, len(rawModels))
	var cleaned []string
	for _, item := range rawModels {
		norm := NormalizeModelID(item)
		if norm == "" || seen[norm] {
			continue
		}
		seen[norm] = true
		cleaned = append(cleaned, norm)
	}
	return cleaned
}

func AllowsModel(mode ModelSelectionMode, selected []string, modelID string) bool {
	target := NormalizeModelID(modelID)
	if target == "" {
		return false
	}
	role, err := ParseServingRole(string(mode))
	if err != nil {
		return false
	}
	if role == ModelModeAuto {
		return true
	}
	normSel := deduplicateNormalizedModels(selected)
	if len(normSel) == 0 {
		return false
	}
	for _, s := range normSel {
		if s == target {
			return true
		}
	}
	return false
}

func WorkerHasCachedSHA256(cached []string, sha256Hex string) bool {
	target := strings.ToLower(strings.TrimSpace(sha256Hex))
	if target == "" {
		return false
	}
	for _, item := range cached {
		if strings.EqualFold(strings.TrimSpace(item), target) {
			return true
		}
	}
	return false
}

type WorkerUIMode string

const (
	WorkerUISimple   WorkerUIMode = "simple"
	WorkerUIAdvanced WorkerUIMode = "advanced"
)

func ParseWorkerUIMode(raw string) (WorkerUIMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(WorkerUISimple):
		return WorkerUISimple, nil
	case string(WorkerUIAdvanced):
		return WorkerUIAdvanced, nil
	default:
		return "", derr.New(derr.CodeValidation, 400, "uiMode must be simple or advanced", nil)
	}
}

func ParseWorkerLanguage(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "en", "en-us", "en-gb":
		return "en", nil
	case "pt", "pt-br":
		return "pt-BR", nil
	default:
		return "", derr.New(derr.CodeValidation, 400, "language must be en or pt-BR", nil)
	}
}

type WorkerAccountAuth struct {
	AccountEmail string
	WorkerToken  string
}

type WorkerPersistedConfig struct {
	GatewayURL       string   `json:"gatewayUrl"`
	GatewayPinned    bool     `json:"gatewayPinned,omitempty"`
	BootstrapVersion uint64   `json:"bootstrapVersion,omitempty"`
	BootstrapURLs    []string `json:"bootstrapUrls,omitempty"`
	WorkerToken      string   `json:"workerToken"`
	AccountEmail     string   `json:"accountEmail,omitempty"`
	WorkerName       string   `json:"workerName"`
	Currency         string   `json:"currency,omitempty"`
	SelectedIDs      []string `json:"selectedIds"`
	ScheduleMode     string   `json:"scheduleMode,omitempty"`
	IdleThresholdSec int      `json:"idleThresholdSec,omitempty"`
	ModelMode        string   `json:"modelMode,omitempty"`
	SelectedModels   []string `json:"selectedModels,omitempty"`
	EnabledBackends  []string `json:"enabledBackends,omitempty"`
	Language         string   `json:"language,omitempty"`
	UIMode           string   `json:"uiMode,omitempty"`
}
