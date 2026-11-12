package worker

import (
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

func resolveWorkerDataDir(configured string, store ConfigStore) string {
	if trimmed := strings.TrimSpace(configured); trimmed != "" {
		return trimmed
	}
	if store != nil {
		if sd := strings.TrimSpace(store.DataDir()); sd != "" {
			return sd
		}
	}
	return "gpu-grid-worker"
}

func (e *Engine) recordConfigFieldWarning(field string, err error) {
	if err == nil {
		return
	}
	e.lastError = err.Error()
	e.logger.Warn("worker_config_field_invalid", "field", field, "error", err.Error())
}

func (e *Engine) loadPersistedConfig() {
	if e.configStore == nil {
		return
	}
	cfg, found, err := e.configStore.LoadConfig()
	if err != nil {
		e.lastError = "invalid worker config.json: " + err.Error()
		e.logger.Warn("worker_config_parse_failed", "error", err.Error())
		return
	}
	if !found {
		return
	}
	e.applyPersistedFields(cfg)
}

func (e *Engine) applyPersistedFields(cfg PersistedConfig) {
	e.applyPersistedBootstrapState(cfg)
	e.applyPersistedEndpointAndIdentity(cfg)
	e.applyPersistedRoutingAndSchedule(cfg)
	e.applyPersistedUIAndAccount(cfg)
}

func (e *Engine) applyPersistedBootstrapState(cfg PersistedConfig) {
	e.gatewayPinned = cfg.GatewayPinned
	e.bootstrapVersion = cfg.BootstrapVersion
	if len(cfg.BootstrapURLs) > 0 {
		normURLs, err := nodecore.NormalizeBootstrapDiscoveryURLList(cfg.BootstrapURLs, e.allowInsecureWS)
		if err != nil {
			e.recordConfigFieldWarning("bootstrapUrls", err)
			return
		}
		e.discoveredBootstrapURLs = normURLs
	}
}

func (e *Engine) applyPersistedUIAndAccount(cfg PersistedConfig) {
	if strings.TrimSpace(cfg.AccountEmail) != "" && strings.TrimSpace(e.workerToken) != "" {
		e.accountEmail = strings.TrimSpace(cfg.AccountEmail)
	}
	if strings.TrimSpace(cfg.UIMode) != "" {
		ui, err := nodecore.ParseWorkerUIMode(cfg.UIMode)
		if err != nil {
			e.recordConfigFieldWarning("uiMode", err)
			return
		}
		e.uiMode = string(ui)
	}
}

func (e *Engine) applyPersistedEndpointAndIdentity(cfg PersistedConfig) {
	if strings.TrimSpace(cfg.GatewayURL) != "" {
		if normGW, err := e.validateAndNormalizeGateway(cfg.GatewayURL); err != nil {
			e.recordConfigFieldWarning("gatewayUrl", err)
		} else {
			e.gatewayURL = normGW
		}
	}
	if strings.TrimSpace(cfg.WorkerToken) != "" {
		e.workerToken = strings.TrimSpace(cfg.WorkerToken)
	}
	if strings.TrimSpace(cfg.WorkerName) != "" {
		e.workerName = strings.TrimSpace(cfg.WorkerName)
	}
	if strings.TrimSpace(cfg.Currency) != "" {
		if parsed, err := nodecore.ParseDisplayCurrency(cfg.Currency); err != nil {
			e.recordConfigFieldWarning("currency", err)
		} else {
			e.currency = parsed
		}
	}
	if len(cfg.SelectedIDs) > 0 {
		e.selectedIDs = append([]string(nil), cfg.SelectedIDs...)
	}
}

func (e *Engine) applyPersistedRoutingAndSchedule(cfg PersistedConfig) {
	if strings.TrimSpace(cfg.ScheduleMode) != "" {
		if sched, err := nodecore.ParseIdleMode(cfg.ScheduleMode); err != nil {
			e.recordConfigFieldWarning("scheduleMode", err)
		} else {
			e.scheduleMode = string(sched)
			e.scheduleConfigured = true
		}
	}
	if cfg.IdleThresholdSec > 0 {
		e.idleThresholdSec = ClampIdleThreshold(cfg.IdleThresholdSec)
	}
	if strings.TrimSpace(cfg.ModelMode) != "" || len(cfg.SelectedModels) > 0 {
		m, sel, err := parseModelPreferencesPreservingSelection(cfg.ModelMode, cfg.SelectedModels)
		if err != nil {
			e.recordConfigFieldWarning("modelMode", err)
		} else {
			e.modelMode = string(m)
			e.selectedModels = sel
		}
	}
	if cfg.EnabledBackends != nil {
		e.enabledBackendsSet = true
		e.enabledBackends = NormalizeBackendList(cfg.EnabledBackends)
	}
	if strings.TrimSpace(cfg.Language) != "" {
		if parsedLang, err := nodecore.ParseWorkerLanguage(cfg.Language); err != nil {
			e.recordConfigFieldWarning("language", err)
		} else {
			e.language = parsedLang
		}
	}
}

func (e *Engine) applyBootOverrides(boot BootConfig) {
	e.allowInsecureWS = boot.AllowInsecureWS
	e.applyBootBootstrapOverrides(boot)
	if strings.TrimSpace(boot.GatewayURL) != "" {
		if normGW, err := e.validateAndNormalizeGateway(boot.GatewayURL); err != nil {
			e.recordConfigFieldWarning("gatewayUrl", err)
		} else {
			e.gatewayURL = normGW
			e.bootGatewayPinned = true
		}
	}
	if bootTok := strings.TrimSpace(boot.Token); bootTok != "" {
		if bootTok != e.workerToken {
			e.accountEmail = ""
		}
		e.workerToken = bootTok
	}
	if strings.TrimSpace(boot.WorkerName) != "" {
		e.workerName = strings.TrimSpace(boot.WorkerName)
	}
	if len(boot.Devices) > 0 {
		e.selectedIDs = append([]string(nil), boot.Devices...)
	}
	if boot.EnabledBackends != nil {
		e.enabledBackendsSet = true
		e.enabledBackends = NormalizeBackendList(boot.EnabledBackends)
	}
	e.cdnModelsBaseURL = strings.TrimRight(strings.TrimSpace(boot.CDNModelsBaseURL), "/")
	e.applyBootScheduleAndTimers(boot)
}

func (e *Engine) applyBootBootstrapOverrides(boot BootConfig) {
	if !boot.BootstrapURLsExplicit {
		return
	}
	normURLs, err := nodecore.NormalizeBootstrapDiscoveryURLList(boot.BootstrapURLs, e.allowInsecureWS)
	if err != nil {
		e.recordConfigFieldWarning("bootstrapUrls", err)
		return
	}
	e.discoveredBootstrapURLs = nil
	e.bootstrapURLs = normURLs
}

func (e *Engine) applyBootScheduleAndTimers(boot BootConfig) {
	if strings.TrimSpace(boot.ModelMode) != "" || len(boot.SelectedModels) > 0 {
		m, sel, err := parseModelPreferencesPreservingSelection(boot.ModelMode, boot.SelectedModels)
		if err != nil {
			e.recordConfigFieldWarning("modelMode", err)
		} else {
			e.modelMode = string(m)
			e.selectedModels = sel
		}
	}
	if strings.TrimSpace(boot.ScheduleMode) != "" {
		if sched, err := nodecore.ParseIdleMode(boot.ScheduleMode); err != nil {
			e.recordConfigFieldWarning("scheduleMode", err)
		} else {
			e.scheduleMode = string(sched)
			e.scheduleConfigured = true
		}
	}
	if boot.IdleThresholdSec > 0 {
		e.idleThresholdSec = ClampIdleThreshold(boot.IdleThresholdSec)
	}
	if boot.ReconnectDelay > 0 {
		e.reconnectDelay = boot.ReconnectDelay
	}
	if boot.IdlePollInterval > 0 {
		e.idlePollInterval = boot.IdlePollInterval
	}
	if boot.PauseDrainTimeout > 0 {
		e.pauseDrainTimeout = boot.PauseDrainTimeout
	}
}

func (e *Engine) buildPersistedConfigLocked() PersistedConfig {
	return PersistedConfig{
		GatewayURL:       e.gatewayURL,
		GatewayPinned:    e.gatewayPinned,
		BootstrapVersion: e.bootstrapVersion,
		BootstrapURLs:    append([]string(nil), e.discoveredBootstrapURLs...),
		WorkerToken:      e.workerToken,
		AccountEmail:     e.accountEmail,
		WorkerName:       e.workerName,
		Currency:         string(e.currency),
		SelectedIDs:      append([]string(nil), e.selectedIDs...),
		ScheduleMode:     e.scheduleMode,
		IdleThresholdSec: e.idleThresholdSec,
		ModelMode:        e.modelMode,
		SelectedModels:   append([]string(nil), e.selectedModels...),
		EnabledBackends:  append([]string(nil), e.enabledBackends...),
		Language:         e.language,
		UIMode:           e.uiMode,
	}
}

func (e *Engine) snapshotPersistedConfig() PersistedConfig {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.buildPersistedConfigLocked()
}

func (e *Engine) persistConfig() {
	if e.configStore == nil {
		return
	}
	e.cfgWriteMu.Lock()
	defer e.cfgWriteMu.Unlock()

	cfg := e.snapshotPersistedConfig()
	if err := e.configStore.SaveConfig(cfg); err != nil {
		e.logger.Warn("worker_config_write_failed", "error", err.Error())
	}
}

func (e *Engine) validateAndNormalizeGateway(raw string) (string, error) {
	if e.gatewayClient != nil {
		return e.gatewayClient.NormalizeGatewayURL(raw)
	}
	return nodecore.NormalizeGatewayURL(raw)
}
