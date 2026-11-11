package worker

import (
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type mutableConfigState struct {
	language           string
	uiMode             string
	gatewayURL         string
	workerToken        string
	accountEmail       string
	workerName         string
	selectedIDs        []string
	scheduleMode       string
	scheduleConfigured bool
	idleThresholdSec   int
	manualPaused       bool
	modelMode          string
	selectedModels     []string
	enabledBackends    []string
	enabledBackendsSet bool
}

type settingsCommitEffects struct {
	backendsChanged bool
	modelsChanged   bool
	schedChanged    bool
	prevPaused      bool
	wasDraining     bool
	backends        []string
	modelMode       string
	selectedModels  []string
	sendFn          func(protocol.ControlMsg) error
	persistedConfig PersistedConfig
}

func wantsScheduleMutation(s UpdateSettings) bool {
	return s.ScheduleModeSet || strings.TrimSpace(s.ScheduleMode) != "" || s.IdleThresholdSet || s.IdleThresholdSec != 0 || s.ManualPaused != nil
}

func wantsRunningLockedMutation(s UpdateSettings) bool {
	return s.GatewayURLSet || strings.TrimSpace(s.GatewayURL) != "" ||
		s.WorkerTokenSet || strings.TrimSpace(s.WorkerToken) != "" ||
		s.WorkerNameSet || strings.TrimSpace(s.WorkerName) != "" ||
		s.SelectedIDsSet || len(s.SelectedIDs) > 0
}

func (e *Engine) captureMutableConfigLocked() mutableConfigState {
	return mutableConfigState{
		language:           e.language,
		uiMode:             e.uiMode,
		gatewayURL:         e.gatewayURL,
		workerToken:        e.workerToken,
		accountEmail:       e.accountEmail,
		workerName:         e.workerName,
		selectedIDs:        append([]string(nil), e.selectedIDs...),
		scheduleMode:       e.scheduleMode,
		scheduleConfigured: e.scheduleConfigured,
		idleThresholdSec:   e.idleThresholdSec,
		manualPaused:       e.manualPaused,
		modelMode:          e.modelMode,
		selectedModels:     append([]string(nil), e.selectedModels...),
		enabledBackends:    append([]string(nil), e.enabledBackends...),
		enabledBackendsSet: e.enabledBackendsSet,
	}
}

func (e *Engine) restoreMutableConfigLocked(st mutableConfigState) {
	e.language = st.language
	e.uiMode = st.uiMode
	e.gatewayURL = st.gatewayURL
	e.workerToken = st.workerToken
	e.accountEmail = st.accountEmail
	e.workerName = st.workerName
	e.selectedIDs = append([]string(nil), st.selectedIDs...)
	e.scheduleMode = st.scheduleMode
	e.scheduleConfigured = st.scheduleConfigured
	e.idleThresholdSec = st.idleThresholdSec
	e.manualPaused = st.manualPaused
	e.modelMode = st.modelMode
	e.selectedModels = append([]string(nil), st.selectedModels...)
	e.enabledBackends = append([]string(nil), st.enabledBackends...)
	e.enabledBackendsSet = st.enabledBackendsSet
}

func (e *Engine) ApplySettings(settings UpdateSettings) error {
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()

	schedMut := wantsScheduleMutation(settings)
	var idleNow int
	if schedMut {
		e.pauseMu.Lock()
		defer e.pauseMu.Unlock()
		idleNow = e.sampleIdleSeconds()
	}

	normGW, err := e.prevalidateGatewaySetting(settings)
	if err != nil {
		return err
	}

	effects, err := e.commitSettingsTransaction(settings, normGW, schedMut)
	if err != nil {
		return err
	}

	return e.dispatchPostCommitEffects(effects, idleNow)
}

func (e *Engine) prevalidateGatewaySetting(s UpdateSettings) (string, error) {
	if !s.GatewayURLSet && strings.TrimSpace(s.GatewayURL) == "" {
		return "", nil
	}
	if strings.TrimSpace(s.GatewayURL) == "" {
		return "", derr.New(derr.CodeValidation, 400, "gatewayUrl cannot be empty", nil)
	}
	return e.validateAndNormalizeGateway(s.GatewayURL)
}

func (e *Engine) commitSettingsTransaction(s UpdateSettings, normGW string, schedMut bool) (settingsCommitEffects, error) {
	e.cfgWriteMu.Lock()
	defer e.cfgWriteMu.Unlock()

	e.mu.Lock()
	if e.running && wantsRunningLockedMutation(s) {
		e.mu.Unlock()
		return settingsCommitEffects{}, derr.New(derr.CodeIllegalState, 409, "stop the worker before changing gateway URL, token, name, or GPU devices", nil)
	}
	prev := e.captureMutableConfigLocked()
	prevPaused, _ := e.effectivePauseLocked()
	wasDraining := e.pauseDraining

	cand, backendsChanged, modelsChanged, err := buildValidatedConfigCandidate(prev, e.detectedGPUs, s, normGW)
	if err != nil {
		e.mu.Unlock()
		return settingsCommitEffects{}, err
	}

	e.restoreMutableConfigLocked(cand)
	effects := settingsCommitEffects{
		backendsChanged: backendsChanged,
		modelsChanged:   modelsChanged,
		schedChanged:    schedMut,
		prevPaused:      prevPaused,
		wasDraining:     wasDraining,
		backends:        e.effectiveBackendsLocked(),
		modelMode:       e.modelMode,
		selectedModels:  append([]string(nil), e.selectedModels...),
		sendFn:          e.activeSendJSON,
		persistedConfig: e.buildPersistedConfigLocked(),
	}
	e.mu.Unlock()

	if e.configStore != nil {
		if saveErr := e.configStore.SaveConfig(effects.persistedConfig); saveErr != nil {
			e.mu.Lock()
			e.restoreMutableConfigLocked(prev)
			e.mu.Unlock()
			return settingsCommitEffects{}, derr.Wrap(derr.CodeInternal, 500, "failed to persist worker configuration", saveErr)
		}
	}
	return effects, nil
}

func (e *Engine) dispatchPostCommitEffects(fx settingsCommitEffects, idleNow int) error {
	if fx.backendsChanged {
		e.syncSoloEnabledBackends()
		fx.backends = e.refreshEffectiveBackendsFromSupervisor()
		e.reconcileIdleRPCForBackends(fx.backends)
	}
	if fx.schedChanged {
		plan := e.buildCommittedSchedulePlan(fx.prevPaused, fx.wasDraining, idleNow)
		if plan.logUnavailable {
			e.logIdleBackendUnavailable()
		}
		e.executePauseTransition(plan, false)
	}
	var sendErr error
	if (fx.modelsChanged || fx.backendsChanged) && fx.sendFn != nil {
		wireMode, wireModels := canonicalWireModelSelection(fx.modelMode, fx.selectedModels)
		if err := fx.sendFn(protocol.ControlMsg{
			Type:              protocol.ControlUpdateModels,
			ModelMode:         wireMode,
			SelectedModels:    wireModels,
			SupportedBackends: fx.backends,
		}); err != nil {
			e.logger.Warn("worker_update_models_send_failed", "error", err.Error())
			sendErr = err
		} else if fx.modelsChanged {
			e.addLog("Model routing updated live -> mode=%s models=%v", strings.ToUpper(wireMode), wireModels)
		}
	}
	e.notifyObservers()
	return sendErr
}

func (e *Engine) refreshEffectiveBackendsFromSupervisor() []string {
	e.mu.Lock()
	curDir := e.runtimeDir
	e.mu.Unlock()
	if curDir == "" {
		curDir = e.ResolveExistingRuntimeDir()
	}
	probed := e.probeBackendsForRuntime(curDir)
	e.mu.Lock()
	e.probedBackends = probed
	effective := e.effectiveBackendsLocked()
	e.mu.Unlock()
	return effective
}

func buildValidatedConfigCandidate(
	prev mutableConfigState,
	detected []protocol.GpuInfo,
	s UpdateSettings,
	normGW string,
) (mutableConfigState, bool, bool, error) {
	cand := prev
	if err := applyValidatedIdentityAndAppearance(&cand, detected, s, normGW); err != nil {
		return cand, false, false, err
	}
	if err := applyValidatedScheduleCandidate(&cand, s); err != nil {
		return cand, false, false, err
	}
	modelsChanged, err := applyValidatedModelsCandidate(&cand, s)
	if err != nil {
		return cand, false, false, err
	}
	backendsChanged, err := applyValidatedBackendsCandidate(&cand, s)
	if err != nil {
		return cand, false, false, err
	}
	return cand, backendsChanged, modelsChanged, nil
}

func applyValidatedIdentityAndAppearance(cand *mutableConfigState, detected []protocol.GpuInfo, s UpdateSettings, normGW string) error {
	if normGW != "" {
		cand.gatewayURL = normGW
	}
	if err := applyValidatedTokenAndName(cand, s); err != nil {
		return err
	}
	if s.SelectedIDsSet || len(s.SelectedIDs) > 0 {
		devs, err := validateSelectedDeviceIDs(detected, s.SelectedIDs)
		if err != nil {
			return err
		}
		cand.selectedIDs = devs
	}
	return applyValidatedLocaleAndUI(cand, s)
}

func applyValidatedTokenAndName(cand *mutableConfigState, s UpdateSettings) error {
	if s.WorkerTokenSet || strings.TrimSpace(s.WorkerToken) != "" {
		tok := strings.TrimSpace(s.WorkerToken)
		if tok == "" {
			return derr.New(derr.CodeValidation, 400, "workerToken cannot be empty", nil)
		}
		if tok != cand.workerToken {
			cand.accountEmail = ""
		}
		cand.workerToken = tok
	}
	if s.WorkerNameSet || strings.TrimSpace(s.WorkerName) != "" {
		name := strings.TrimSpace(s.WorkerName)
		if name == "" {
			return derr.New(derr.CodeValidation, 400, "workerName cannot be empty", nil)
		}
		cand.workerName = name
	}
	return nil
}

func applyValidatedLocaleAndUI(cand *mutableConfigState, s UpdateSettings) error {
	if s.LanguageSet || strings.TrimSpace(s.Language) != "" {
		lang, err := nodecore.ParseWorkerLanguage(s.Language)
		if err != nil {
			return err
		}
		cand.language = lang
	}
	if s.UIModeSet || strings.TrimSpace(s.UIMode) != "" {
		ui, err := nodecore.ParseWorkerUIMode(s.UIMode)
		if err != nil {
			return err
		}
		cand.uiMode = string(ui)
	}
	return nil
}

func validateSelectedDeviceIDs(detected []protocol.GpuInfo, raw []string) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, id := range raw {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			return nil, derr.New(derr.CodeValidation, 400, "selectedIds cannot contain empty device IDs", nil)
		}
		resolved, ok := matchDetectedOrAcceptUnprobed(detected, trimmed)
		if !ok {
			return nil, derr.Validationf(derr.CodeValidation, "unknown GPU device ID %q", trimmed)
		}
		key := strings.ToLower(resolved)
		if !seen[key] {
			seen[key] = true
			out = append(out, resolved)
		}
	}
	return out, nil
}

func matchDetectedOrAcceptUnprobed(detected []protocol.GpuInfo, id string) (string, bool) {
	if len(detected) == 0 {
		return id, true
	}
	for _, d := range detected {
		if strings.EqualFold(d.DeviceID, id) {
			return d.DeviceID, true
		}
	}
	return "", false
}

func applyValidatedScheduleCandidate(cand *mutableConfigState, s UpdateSettings) error {
	if s.ScheduleModeSet || strings.TrimSpace(s.ScheduleMode) != "" {
		if strings.TrimSpace(s.ScheduleMode) == "" {
			return derr.New(derr.CodeValidation, 400, "scheduleMode cannot be empty", nil)
		}
		sched, err := nodecore.ParseIdleMode(s.ScheduleMode)
		if err != nil {
			return err
		}
		cand.scheduleMode = string(sched)
		cand.scheduleConfigured = true
	}
	if s.IdleThresholdSet || s.IdleThresholdSec != 0 {
		if s.IdleThresholdSec < MinIdleThresholdSec || s.IdleThresholdSec > MaxIdleThresholdSec {
			return derr.New(derr.CodeValidation, 400, "idleThresholdSec must be between 15 and 3600", nil)
		}
		cand.idleThresholdSec = s.IdleThresholdSec
	}
	if s.ManualPaused != nil {
		cand.manualPaused = *s.ManualPaused
	}
	return nil
}

func applyValidatedModelsCandidate(cand *mutableConfigState, s UpdateSettings) (bool, error) {
	if !s.ModelSelectionSet && !s.ModelModeSet && !s.SelectedModelsSet {
		return false, nil
	}
	mode := cand.modelMode
	if s.ModelModeSet {
		if strings.TrimSpace(s.ModelMode) == "" {
			return false, derr.New(derr.CodeValidation, 400, "modelMode cannot be empty", nil)
		}
		mode = s.ModelMode
	} else if s.ModelSelectionSet {
		mode = s.ModelMode
	}
	sel := resolveCandidateSelectedModels(cand.selectedModels, s)
	normMode, normSel, err := parseModelPreferencesPreservingSelection(mode, sel)
	if err != nil {
		return false, err
	}
	cand.modelMode = string(normMode)
	cand.selectedModels = normSel
	return true, nil
}

func resolveCandidateSelectedModels(existing []string, s UpdateSettings) []string {
	if s.SelectedModelsSet {
		return s.SelectedModels
	}
	if s.ModelSelectionSet {
		role, err := nodecore.ParseServingRole(s.ModelMode)
		if err != nil || role == nodecore.ModelModeMulti || len(s.SelectedModels) > 0 {
			return s.SelectedModels
		}
	}
	return existing
}

func parseModelPreferencesPreservingSelection(rawMode string, rawModels []string) (nodecore.ModelSelectionMode, []string, error) {
	role, err := nodecore.ParseServingRole(rawMode)
	if err != nil {
		return "", nil, err
	}
	cleaned := normalizeSelectedModelIDs(rawModels)
	if role == nodecore.ModelModeMulti && len(cleaned) == 0 {
		return "", nil, derr.New(derr.CodeValidation, 400, "multi serving role requires at least one selected model", nil)
	}
	return role, cleaned, nil
}

func normalizeSelectedModelIDs(rawModels []string) []string {
	seen := make(map[string]bool, len(rawModels))
	var cleaned []string
	for _, item := range rawModels {
		norm := nodecore.NormalizeModelID(item)
		if norm == "" || seen[norm] {
			continue
		}
		seen[norm] = true
		cleaned = append(cleaned, norm)
	}
	return cleaned
}

func canonicalWireModelSelection(mode string, selected []string) (string, []string) {
	if normMode, normModels, err := nodecore.ParseModelSelection(mode, selected); err == nil {
		return string(normMode), normModels
	}
	return mode, append([]string(nil), selected...)
}

func applyValidatedBackendsCandidate(cand *mutableConfigState, s UpdateSettings) (bool, error) {
	if !s.BackendsSet {
		return false, nil
	}
	seen := make(map[string]bool, len(s.EnabledBackends))
	validated := make([]string, 0, len(s.EnabledBackends))
	for _, raw := range s.EnabledBackends {
		parsed, err := nodecore.ParseEngineBackend(raw)
		if err != nil {
			return false, err
		}
		val := parsed.String()
		if !seen[val] {
			seen[val] = true
			validated = append(validated, val)
		}
	}
	cand.enabledBackendsSet = true
	cand.enabledBackends = validated
	return true, nil
}
