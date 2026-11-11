package workergui

import (
	"fmt"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	. "modernc.org/tk9.0"
)

func ResolveBannerContent(lang, uiErr string, s worker.StatusSnapshot) (string, bool) {
	if s.LastError != "" {
		return FormatWorkerError(lang, s.LastError), true
	}
	if uiErr != "" {
		return FormatWorkerError(lang, uiErr), true
	}
	if s.Running && isPauseDrainingSnapshot(s) {
		return fmt.Sprintf(LabelsFor(lang).PauseDrainingBanner, resolveBannerPauseReason(lang, s.PauseReason)), false
	}
	if s.Running && s.Paused && !isActiveAutoPreDownloadSnapshot(s) {
		return fmt.Sprintf(LabelsFor(lang).PausedBanner, resolveBannerPauseReason(lang, s.PauseReason)), false
	}
	if s.WaitingReason != "" {
		return FormatWaitingReason(lang, s.WaitingReason), false
	}
	if s.Running && s.IdleStatus != "" {
		return FormatPauseReason(lang, s.IdleStatus), false
	}
	return "", false
}

func isActiveAutoPreDownloadSnapshot(s worker.StatusSnapshot) bool {
	return !s.ManualPaused &&
		strings.EqualFold(strings.TrimSpace(s.Status), worker.StatusPreDownloading) &&
		strings.TrimSpace(s.PreDownloadModelID) != ""
}

func isPauseDrainingSnapshot(s worker.StatusSnapshot) bool {
	return s.PauseDraining || strings.EqualFold(strings.TrimSpace(s.Status), worker.StatusPauseDraining)
}

func resolveBannerPauseReason(lang, raw string) string {
	reason := FormatPauseReason(lang, raw)
	if reason == "" {
		return FormatStatusLabel(lang, "PAUSED")
	}
	return reason
}

func (g *guiController) syncBanner(s worker.StatusSnapshot) {
	msg, isError := ResolveBannerContent(g.lang, g.uiErr, s)
	if msg != "" {
		sig := fmt.Sprintf("%v|%s", isError, msg)
		if sig != g.widgetCache.bannerSig {
			g.widgetCache.bannerSig = sig
			bg, fg := "#3b2a11", "#fcd34d"
			if isError {
				bg, fg = "#3b1219", "#fca5a5"
			}
			g.errBanner.Configure(Background(bg), Foreground(fg), Txt(msg))
		}
		if !g.errVisible {
			Grid(g.errBanner, Row(1), Column(0), Sticky("ew"), Pady("0 8"))
			g.errVisible = true
		}
		return
	}
	g.widgetCache.bannerSig = ""
	if g.errVisible {
		GridForget(g.errBanner.Window)
		g.errVisible = false
	}
}

func (g *guiController) syncRunningControls(s worker.StatusSnapshot) {
	draining := isPauseDrainingSnapshot(s)
	sig := fmt.Sprintf("%s|%v|%v|%v|%v|%v", g.lang, s.Running, g.isScanning, draining, s.ManualPaused, s.Paused)
	if sig == g.widgetCache.runningControlsSig {
		return
	}
	g.widgetCache.runningControlsSig = sig
	l := LabelsFor(g.lang)
	if s.Running {
		g.gwEntry.Configure(State("disabled"))
		g.tokEntry.Configure(State("disabled"))
		g.nameEntry.Configure(State("disabled"))
		g.scanBtn.Configure(State("disabled"), Txt(l.ScanGPUs))
		g.syncPauseButton(s, draining, l)
		g.toggleBtn.Configure(Style("Stop.TButton"), Txt(l.StopBtn))
		return
	}
	g.gwEntry.Configure(State("normal"))
	g.tokEntry.Configure(State("normal"))
	g.nameEntry.Configure(State("normal"))
	g.pauseBtn.Configure(State("disabled"), Txt(l.PauseOffer))
	if g.isScanning {
		g.scanBtn.Configure(State("disabled"), Txt(l.Scanning))
	} else {
		g.scanBtn.Configure(State("normal"), Txt(l.ScanGPUs))
	}
	g.toggleBtn.Configure(Style("Start.TButton"), Txt(l.StartBtn))
}

func (g *guiController) syncPauseButton(s worker.StatusSnapshot, draining bool, l GUILabels) {
	if draining && s.ManualPaused {
		g.pauseBtn.Configure(State("normal"), Txt(l.PauseDrainingBtn))
		return
	}
	if draining {
		g.pauseBtn.Configure(State("disabled"), Txt(l.PausingOffer))
		return
	}
	if s.ManualPaused {
		g.pauseBtn.Configure(State("normal"), Txt(l.ResumeOffer))
		return
	}
	if s.Paused {
		g.pauseBtn.Configure(State("normal"), Txt(l.AutoPausedBtn))
		return
	}
	g.pauseBtn.Configure(State("normal"), Txt(l.PauseOffer))
}

func (g *guiController) readValidatedScheduleInputs() (string, int, bool) {
	sched := "always"
	if g.idleCb.Variable() == "1" {
		sched = "idle"
	}
	rawSec := strings.TrimSpace(g.idleSecEntry.Textvariable())
	if sched == "always" && rawSec == "" {
		return sched, 0, true
	}
	idleSec, ok := ParseIdleSecInput(rawSec)
	if !ok {
		g.uiErr = LabelsFor(g.lang).InvalidIdleErr
		g.syncUI()
		return sched, 0, false
	}
	return sched, idleSec, true
}

func (g *guiController) handleScheduleChange(togglePause bool) {
	snap := g.eng.Snapshot()
	if togglePause {
		nextManual := !snap.ManualPaused
		g.uiErr = ""
		go g.eng.UpdateSchedule(snap.ScheduleMode, snap.IdleThresholdSec, &nextManual)
		return
	}
	mode := "always"
	if g.idleCb.Variable() == "1" {
		mode = "idle"
	}
	rawSec := strings.TrimSpace(g.idleSecEntry.Textvariable())
	sec := snap.IdleThresholdSec
	if mode == "idle" || rawSec != "" {
		parsedSec, secOK := ParseIdleSecInput(rawSec)
		if !secOK {
			g.idleCb.Configure(Variable(boolCheckVar(snap.ScheduleMode == "idle")))
			g.uiErr = LabelsFor(g.lang).InvalidIdleErr
			g.syncUI()
			return
		}
		sec = parsedSec
	}
	g.uiErr = ""
	g.commitScheduleBaseline(mode, sec)
	go g.eng.UpdateSchedule(mode, sec, nil)
}

func (g *guiController) handleStartStopToggle() {
	if g.eng.Snapshot().Running {
		g.widgetCache.runningControlsSig = ""
		g.toggleBtn.Configure(State("disabled"))
		go func() {
			g.eng.Stop("Worker stopped by user.")
			g.postUI(func() {
				g.widgetCache.runningControlsSig = ""
				g.toggleBtn.Configure(State("normal"))
				g.syncUI()
			}, false)
		}()
		return
	}
	g.startWorkerFromForm()
}

func (g *guiController) startWorkerFromForm() {
	sched, idleSec, ok := g.readValidatedScheduleInputs()
	if !ok {
		return
	}
	g.uiErr = ""
	params := worker.StartParams{
		GatewayURL:        strings.TrimSpace(g.gwEntry.Textvariable()),
		WorkerToken:       strings.TrimSpace(g.tokEntry.Textvariable()),
		WorkerName:        strings.TrimSpace(g.nameEntry.Textvariable()),
		SelectedIDs:       g.readCheckedGPUs(),
		ScheduleMode:      sched,
		IdleThresholdSec:  idleSec,
		ModelMode:         g.modelMode,
		SelectedModels:    g.selectedModelSlice(),
		ModelSelectionSet: true,
	}
	g.widgetCache.runningControlsSig = ""
	g.toggleBtn.Configure(State("disabled"))
	go func() {
		err := g.eng.Start(params)
		g.postUI(func() {
			g.setActionError(err)
			if err == nil {
				snap := g.eng.Snapshot()
				g.syncWidgetsFromSnapshot(snap)
			}
			g.widgetCache.runningControlsSig = ""
			g.toggleBtn.Configure(State("normal"))
			g.syncUI()
		}, false)
	}()
}
