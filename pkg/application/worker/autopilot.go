package worker

import (
	"context"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func (e *Engine) SetUIMode(mode string) string {
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()

	parsed, err := nodecore.ParseWorkerUIMode(mode)
	if err != nil {
		e.mu.Lock()
		cur := e.uiMode
		e.lastError = err.Error()
		e.mu.Unlock()
		e.notifyObservers()
		return cur
	}
	resolved := string(parsed)
	e.mu.Lock()
	e.uiMode = resolved
	e.mu.Unlock()
	e.persistConfig()
	e.notifyObservers()
	return resolved
}

func (e *Engine) ClearLogs() {
	e.mu.Lock()
	e.logs = nil
	e.mu.Unlock()
	e.notifyObservers()
}

func (e *Engine) UnlinkAccount() error {
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()

	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return derr.New(derr.CodeIllegalState, 409, "stop the worker before unlinking the account", nil)
	}
	e.workerToken = ""
	e.accountEmail = ""
	e.lastCompletedAuthState = ""
	authCancel := e.clearPendingBrowserAuthLocked()
	e.balance = 0
	e.minObservedBalance = 0
	e.lastWorkerStateSeq = 0
	e.lastStateSeq = 0
	e.lastError = ""
	e.mu.Unlock()

	cancelIfNonNil(authCancel)
	e.persistConfig()
	e.addLog("Account unlinked from Worker.")
	return nil
}

func (e *Engine) StartSimple(ctx context.Context, modelMode string, selectedModels []string) error {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()

	e.mu.Lock()
	alreadyRunning := e.running
	e.mu.Unlock()
	if alreadyRunning {
		return nil
	}

	resolvedMode := strings.TrimSpace(modelMode)
	if resolvedMode == "" {
		resolvedMode = protocol.ModelModeAuto
	}
	if _, _, selErr := nodecore.ParseModelSelection(resolvedMode, selectedModels); selErr != nil {
		e.setLastError(selErr.Error())
		return selErr
	}

	if _, err := e.ensurePreStartGPUsLocked(ctx); err != nil {
		return err
	}
	chosenIDs, schedMode, err := e.resolveSimpleStartPreservedConfigLocked()
	if err != nil {
		return err
	}

	return e.startLocked(StartParams{
		SelectedIDs:       chosenIDs,
		ScheduleMode:      schedMode,
		ModelMode:         resolvedMode,
		SelectedModels:    selectedModels,
		ModelSelectionSet: true,
	})
}

func (e *Engine) ensurePreStartGPUsLocked(ctx context.Context) (bool, error) {
	e.mu.Lock()
	alreadyRunning := e.running
	e.mu.Unlock()
	if alreadyRunning {
		return true, nil
	}

	gwURL, _, hasDetected, err := e.validatePreStartTokenAndGateway()
	if err != nil {
		return false, err
	}
	if e.hardware == nil {
		msg := "HardwareProbeError: hardware inspector is not configured"
		e.mu.Lock()
		e.status = StatusProbeError
		e.lastError = msg
		e.mu.Unlock()
		e.notifyObservers()
		return false, derr.New(derr.CodeNoVulkanGPU, 422, msg, nil)
	}
	if !hasDetected {
		if err := e.ScanGPUs(ctx, gwURL); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (e *Engine) requireAutopilotDeviceIDsLocked() ([]string, error) {
	autoIDs := e.selectAutopilotDeviceIDs()
	if len(autoIDs) > 0 {
		return autoIDs, nil
	}
	msg := "no Vulkan GPUs detected on this machine"
	e.mu.Lock()
	e.status = StatusProbeError
	e.lastError = msg
	e.mu.Unlock()
	e.notifyObservers()
	return nil, derr.New(derr.CodeNoVulkanGPU, 422, msg, nil)
}

func (e *Engine) resolveSimpleStartPreservedConfigLocked() ([]string, string, error) {
	e.mu.Lock()
	matched := matchExistingDeviceIDs(e.detectedGPUs, e.selectedIDs)
	schedConfigured := e.scheduleConfigured
	savedSched := strings.TrimSpace(e.scheduleMode)
	e.mu.Unlock()

	chosenIDs := matched
	if len(chosenIDs) == 0 {
		var err error
		chosenIDs, err = e.requireAutopilotDeviceIDsLocked()
		if err != nil {
			return nil, "", err
		}
	}
	schedMode := string(nodecore.ScheduleIdle)
	if schedConfigured && savedSched != "" {
		schedMode = savedSched
	}
	return chosenIDs, schedMode, nil
}

func (e *Engine) selectAutopilotDeviceIDs() []string {
	e.mu.Lock()
	devices := append([]protocol.GpuInfo(nil), e.detectedGPUs...)
	e.mu.Unlock()
	if len(devices) == 0 {
		return nil
	}
	if dedicated := e.filterDedicatedDevices(devices); len(dedicated) > 0 {
		return dedicated
	}
	return []string{devices[0].DeviceID}
}
