package worker

import (
	"context"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

const (
	IdleStatusBackendUnavailable = "idle_backend_unavailable"
	rpcStreamIdleCutoff          = 1500 * time.Millisecond
)

type pauseTransitionPlan struct {
	running        bool
	prevPaused     bool
	wasDraining    bool
	paused         bool
	draining       bool
	drainGen       uint64
	drainCtx       context.Context
	drainTimeout   time.Duration
	streams        *tunnelStreams
	reason         string
	scheduleMode   string
	backends       []string
	runCtx         context.Context
	sendFn         func(protocol.ControlMsg) error
	logUnavailable bool
}

func resolveIdleStatus(scheduleMode string, idleSec int) string {
	if scheduleMode == string(nodecore.ScheduleIdle) && idleSec < 0 {
		return IdleStatusBackendUnavailable
	}
	return ""
}

func (e *Engine) effectivePauseLocked() (bool, string) {
	if e.manualPaused {
		return true, "Paused manually by provider"
	}
	if e.scheduleMode == string(nodecore.ScheduleIdle) && e.autoPaused {
		return true, "Smart Auto-Pause: local desktop activity detected"
	}
	return false, ""
}

func (e *Engine) setInitialAutoPauseFromIdleLocked(idleNow int) {
	e.autoPaused = e.scheduleMode == string(nodecore.ScheduleIdle) && idleNow >= 0 && idleNow < e.idleThresholdSec
}

func (e *Engine) UpdateSchedule(mode string, idleThresholdSec int, manualPaused *bool) {
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()

	var parsedMode string
	if strings.TrimSpace(mode) != "" {
		sched, err := nodecore.ParseIdleMode(mode)
		if err != nil {
			e.setLastError(err.Error())
			e.logger.Warn("worker_schedule_mode_invalid", "mode", mode, "error", err.Error())
			return
		}
		parsedMode = string(sched)
	}

	e.pauseMu.Lock()
	defer e.pauseMu.Unlock()

	idleNow := e.sampleIdleSeconds()
	plan := e.applyScheduleMutation(parsedMode, idleThresholdSec, manualPaused, idleNow)

	e.persistConfig()
	e.notifyObservers()
	if plan.logUnavailable {
		e.logIdleBackendUnavailable()
	}
	e.executePauseTransition(plan, false)
}

func (e *Engine) buildCommittedSchedulePlan(prevPaused, wasDraining bool, idleNow int) pauseTransitionPlan {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.idleSeconds = idleNow
	e.updateAutoPauseFromIdleLocked(idleNow)
	logUnavail := e.updateIdleStatusLocked(idleNow)
	return e.buildPauseTransitionPlanLocked(prevPaused, wasDraining, logUnavail)
}

func (e *Engine) applyScheduleMutation(parsedMode string, idleThresholdSec int, manualPaused *bool, idleNow int) pauseTransitionPlan {
	e.mu.Lock()
	defer e.mu.Unlock()

	prevPaused, _ := e.effectivePauseLocked()
	wasDraining := e.pauseDraining
	e.idleSeconds = idleNow
	if parsedMode != "" {
		e.scheduleMode = parsedMode
		e.scheduleConfigured = true
	}
	if idleThresholdSec > 0 {
		e.idleThresholdSec = ClampIdleThreshold(idleThresholdSec)
	}
	if manualPaused != nil {
		e.manualPaused = *manualPaused
	}
	e.updateAutoPauseFromIdleLocked(idleNow)
	logUnavail := e.updateIdleStatusLocked(idleNow)

	return e.buildPauseTransitionPlanLocked(prevPaused, wasDraining, logUnavail)
}

func (e *Engine) updateAutoPauseFromIdleLocked(idleNow int) {
	if e.scheduleMode == string(nodecore.ScheduleAlways) {
		e.autoPaused = false
		return
	}
	if idleNow < 0 {
		return
	}
	if idleNow < 5 {
		e.autoPaused = true
	} else if idleNow >= e.idleThresholdSec {
		e.autoPaused = false
	}
}

func (e *Engine) updateIdleStatusLocked(idleNow int) bool {
	next := resolveIdleStatus(e.scheduleMode, idleNow)
	e.idleStatus = next
	if next == IdleStatusBackendUnavailable {
		if !e.idleUnavailableLogged {
			e.idleUnavailableLogged = true
			return true
		}
		return false
	}
	e.idleUnavailableLogged = false
	return false
}

func (e *Engine) logIdleBackendUnavailable() {
	e.logger.Warn("idle_backend_unavailable", "schedule_mode", string(nodecore.ScheduleIdle))
	e.addLog("Idle schedule warning: %s (OS idle sensor unavailable; preserving current state)", IdleStatusBackendUnavailable)
}

func (e *Engine) buildPauseTransitionPlanLocked(prevPaused, wasDraining, logUnavail bool) pauseTransitionPlan {
	paused, reason := e.effectivePauseLocked()
	ts := e.activeStreams
	activeCnt := ts.activeBusyCount(rpcStreamIdleCutoff)

	plan := pauseTransitionPlan{
		running:        e.running,
		prevPaused:     prevPaused,
		wasDraining:    wasDraining,
		paused:         paused,
		streams:        ts,
		reason:         reason,
		scheduleMode:   e.scheduleMode,
		backends:       e.effectiveBackendsLocked(),
		runCtx:         e.runCtx,
		sendFn:         e.activeSendJSON,
		drainTimeout:   e.pauseDrainTimeout,
		logUnavailable: logUnavail,
	}

	if !e.running {
		e.cancelPauseDrainLocked()
		return plan
	}
	if !paused {
		e.cancelPauseDrainLocked()
		e.applyUnpausedFieldsLocked()
		return plan
	}
	if activeCnt > 0 && e.runCtx != nil {
		e.cancelPauseDrainLocked()
		drainCtx, drainCancel := context.WithCancel(e.runCtx)
		e.drainCancel = drainCancel
		e.drainGen++
		e.pauseDraining = true
		e.status = StatusPauseDraining
		e.advanceSessionStateLocked(nodecore.WorkerPauseDraining)
		plan.draining = true
		plan.drainGen = e.drainGen
		plan.drainCtx = drainCtx
		return plan
	}

	e.cancelPauseDrainLocked()
	e.applyPausedFieldsLocked()
	return plan
}

func (e *Engine) applyUnpausedFieldsLocked() {
	if e.sessionState != nodecore.WorkerClosed {
		e.advanceSessionStateLocked(nodecore.WorkerIdle)
	}
	if e.status == "PAUSED" || e.status == StatusPauseDraining {
		if e.activeSendJSON != nil {
			e.status = "CONNECTED"
		} else {
			e.status = "STARTING"
		}
	}
}

func (e *Engine) cancelPauseDrainLocked() {
	if e.drainCancel != nil {
		e.drainCancel()
		e.drainCancel = nil
	}
	e.pauseDraining = false
	e.drainGen++
}

func (e *Engine) applyPausedFieldsLocked() {
	e.pauseDraining = false
	e.status = "PAUSED"
	e.advanceSessionStateLocked(nodecore.WorkerPaused)
	e.poolID = ""
	e.activeModelID = ""
	e.assignedLayers = 0
	e.sharePct = 0
	e.executionMode = ""
	e.soloStatus = SoloStatusIdle
	e.soloModelID = ""
}

func (e *Engine) executePauseTransition(plan pauseTransitionPlan, fromIdleTick bool) {
	if plan.running && plan.paused && plan.draining {
		e.startGracefulPauseDrain(plan)
		return
	}
	if plan.running && plan.paused {
		e.executeImmediatePause(plan, fromIdleTick)
		return
	}
	if plan.running && (plan.prevPaused || plan.wasDraining) && !plan.paused {
		e.executeResumeFromPause(plan, fromIdleTick)
		return
	}
	if !fromIdleTick && plan.sendFn != nil {
		e.sendPauseStateFrame(plan)
	}
}

func (e *Engine) startGracefulPauseDrain(plan pauseTransitionPlan) {
	e.stopMeshPeerModel("pause_drain")
	if plan.sendFn != nil {
		if err := plan.sendFn(protocol.ControlMsg{
			Type:              protocol.ControlPauseState,
			ScheduleMode:      plan.scheduleMode,
			Paused:            true,
			PauseReason:       plan.reason,
			SupportedBackends: plan.backends,
		}); err != nil {
			e.logger.Warn("worker_pause_drain_state_failed", "error", err.Error())
		}
	}
	e.addLog("Entering PAUSE_DRAINING (%s) — draining active streams before releasing VRAM.", plan.reason)
	go e.finishPauseDrain(plan)
}

func (e *Engine) finishPauseDrain(plan pauseTransitionPlan) {
	timeout := plan.drainTimeout
	if timeout <= 0 {
		timeout = DefaultPauseDrainTimeout
	}
	waitCtx, cancel := context.WithTimeout(plan.drainCtx, timeout)
	plan.streams.WaitDrained(waitCtx, rpcStreamIdleCutoff)
	cancel()

	if plan.drainCtx.Err() != nil {
		return
	}

	e.pauseMu.Lock()
	defer e.pauseMu.Unlock()

	e.mu.Lock()
	paused, _ := e.effectivePauseLocked()
	stillCurrent := e.running && paused && e.drainGen == plan.drainGen
	if stillCurrent {
		e.drainCancel = nil
		e.applyPausedFieldsLocked()
	}
	sendFn := e.activeSendJSON
	e.mu.Unlock()

	if !stillCurrent {
		return
	}
	if sendFn != nil {
		if err := sendFn(protocol.ControlMsg{
			Type:              protocol.ControlPauseState,
			ScheduleMode:      plan.scheduleMode,
			Paused:            true,
			PauseReason:       plan.reason,
			SupportedBackends: plan.backends,
		}); err != nil {
			e.logger.Debug("worker_pause_after_drain_send_failed", "error", err.Error())
		}
	}
	e.releaseLocalGPUForPause()
	e.addLog("Graceful pause drain complete -> PAUSED (%s) | GPU VRAM released (0 MiB).", plan.reason)
}

func (e *Engine) executeImmediatePause(plan pauseTransitionPlan, fromIdleTick bool) {
	if plan.sendFn != nil {
		if err := plan.sendFn(protocol.ControlMsg{
			Type:              protocol.ControlPauseState,
			ScheduleMode:      plan.scheduleMode,
			Paused:            true,
			PauseReason:       plan.reason,
			SupportedBackends: plan.backends,
		}); err != nil {
			e.logger.Warn("worker_pause_state_send_failed", "error", err.Error())
		}
	}
	e.releaseLocalGPUForPause()
	if fromIdleTick {
		e.addLog("Smart Auto-Pause: local desktop activity detected — GPU released (0 MiB VRAM).")
	} else if !plan.prevPaused {
		e.addLog("Schedule updated -> PAUSED (%s) | GPU VRAM released (0 MiB).", plan.reason)
	}
}

func (e *Engine) executeResumeFromPause(plan pauseTransitionPlan, fromIdleTick bool) {
	if err := e.resumeLocalGPUFromPause(plan.runCtx); err != nil {
		if plan.runCtx != nil && plan.runCtx.Err() != nil {
			return
		}
		e.logger.Error("worker_resume_rpc_failed", "error", err.Error())
		e.mu.Lock()
		if e.running {
			e.status = "ERROR"
			e.lastError = err.Error()
		}
		e.mu.Unlock()
		e.notifyObservers()
		e.addLog("Failed to respawn ggml-rpc-server on resume: %v", err)
		return
	}
	if plan.sendFn != nil {
		if err := plan.sendFn(protocol.ControlMsg{
			Type:              protocol.ControlPauseState,
			ScheduleMode:      plan.scheduleMode,
			Paused:            false,
			PauseReason:       "",
			SupportedBackends: plan.backends,
		}); err != nil {
			e.logger.Warn("worker_resume_state_send_failed", "error", err.Error())
			return
		}
	}
	if fromIdleTick {
		e.addLog("Idle threshold reached — GPU resumed in Order Book.")
	} else {
		e.addLog("Schedule updated -> ACTIVE (mode: %s)", strings.ToUpper(plan.scheduleMode))
	}
}

func (e *Engine) sendPauseStateFrame(plan pauseTransitionPlan) {
	if err := plan.sendFn(protocol.ControlMsg{
		Type:              protocol.ControlPauseState,
		ScheduleMode:      plan.scheduleMode,
		Paused:            plan.paused,
		PauseReason:       plan.reason,
		SupportedBackends: plan.backends,
	}); err != nil {
		e.logger.Warn("worker_pause_state_send_failed", "error", err.Error())
		return
	}
	if plan.paused {
		e.addLog("Schedule updated -> PAUSED (%s)", plan.reason)
	} else {
		e.addLog("Schedule updated -> ACTIVE (mode: %s)", strings.ToUpper(plan.scheduleMode))
	}
}

func (e *Engine) runIdleMonitor(ctx context.Context) {
	interval := e.idlePollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.evaluateIdleTick(ctx)
		}
	}
}

func (e *Engine) evaluateIdleTick(ctx context.Context) {
	e.pauseMu.Lock()
	defer e.pauseMu.Unlock()

	idleSec := e.sampleIdleSeconds()
	plan, transitioned := e.computeIdleTickTransition(ctx, idleSec)
	if plan.logUnavailable {
		e.logIdleBackendUnavailable()
		e.notifyObservers()
	}
	if !transitioned {
		return
	}
	e.notifyObservers()
	e.executePauseTransition(plan, true)
}

func (e *Engine) computeIdleTickTransition(ctx context.Context, idleSec int) (pauseTransitionPlan, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.idleSeconds = idleSec
	logUnavail := e.updateIdleStatusLocked(idleSec)
	if e.scheduleMode != string(nodecore.ScheduleIdle) || idleSec < 0 {
		return pauseTransitionPlan{logUnavailable: logUnavail}, false
	}

	prevPaused, _ := e.effectivePauseLocked()
	wasDraining := e.pauseDraining
	if !e.autoPaused && idleSec < 5 {
		e.autoPaused = true
	} else if e.autoPaused && idleSec >= e.idleThresholdSec {
		e.autoPaused = false
	}
	nowPaused, _ := e.effectivePauseLocked()
	if nowPaused == prevPaused {
		return pauseTransitionPlan{logUnavailable: logUnavail}, false
	}

	plan := e.buildPauseTransitionPlanLocked(prevPaused, wasDraining, logUnavail)
	plan.runCtx = ctx
	return plan, true
}
