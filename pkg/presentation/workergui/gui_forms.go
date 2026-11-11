package workergui

import (
	"context"
	"strconv"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
	. "modernc.org/tk9.0"
)

func buildConnectionRows(ctrl *guiController, s worker.StatusSnapshot) {
	initIdle := boolCheckVar(s.ScheduleMode == "idle")

	ctrl.lblGW = ctrl.cfgBox.TLabel(Style("Field.TLabel"))
	ctrl.gwEntry = ctrl.cfgBox.TEntry(Textvariable(s.GatewayURL))
	ctrl.lblTok = ctrl.cfgBox.TLabel(Style("Field.TLabel"))
	ctrl.tokEntry = ctrl.cfgBox.TEntry(Textvariable(s.WorkerToken), Show("*"))
	ctrl.lblName = ctrl.cfgBox.TLabel(Style("Field.TLabel"))
	ctrl.nameEntry = ctrl.cfgBox.TEntry(Textvariable(s.WorkerName))

	ctrl.idleCb = ctrl.cfgBox.TCheckbutton(Style("Gpu.TCheckbutton"), Variable(initIdle), Command(func() {
		ctrl.handleScheduleChange(false)
	}))
	ctrl.schedRight = ctrl.cfgBox.TFrame()
	ctrl.lblIdleSec = ctrl.schedRight.TLabel(Style("Field.TLabel"))
	ctrl.idleSecEntry = ctrl.schedRight.TEntry(Textvariable(strconv.Itoa(s.IdleThresholdSec)), Width(7))
	Bind(ctrl.idleSecEntry.Window, "<Return>", Command(func(_ *Event) {
		ctrl.handleIdleSecCommit()
	}))
	Bind(ctrl.idleSecEntry.Window, "<FocusOut>", Command(func(_ *Event) {
		ctrl.handleIdleSecCommit()
	}))
	ctrl.pauseBtn = ctrl.schedRight.TButton(Command(func() {
		ctrl.handleScheduleChange(true)
	}))
	Grid(ctrl.lblIdleSec, Row(0), Column(0), Padx("0 4"))
	Grid(ctrl.idleSecEntry, Row(0), Column(1), Padx("0 6"))
	Grid(ctrl.pauseBtn, Row(0), Column(2))
}

func (g *guiController) handleIdleSecCommit() {
	if g.uiClosed.Load() || g.idleSecEntry == nil {
		return
	}
	raw := strings.TrimSpace(g.idleSecEntry.Textvariable())
	if raw == strings.TrimSpace(g.synced.idleSecText) {
		return
	}
	g.handleScheduleChange(false)
}

func boolCheckVar(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func buildGPUSection(ctx context.Context, ctrl *guiController, l GUILabels) {
	ctrl.lblGPUs = ctrl.cfgBox.TLabel(Style("Field.TLabel"), Txt(l.GPUsLabel))
	ctrl.scanBtn = ctrl.cfgBox.TButton(Txt(l.ScanGPUs), Command(func() {
		if ctrl.isScanning {
			return
		}
		ctrl.isScanning = true
		ctrl.widgetCache.runningControlsSig = ""
		gw := strings.TrimSpace(ctrl.gwEntry.Textvariable())
		ctrl.scanBtn.Configure(State("disabled"), Txt(LabelsFor(ctrl.lang).Scanning))
		go func() {
			err := ctrl.eng.ScanGPUs(ctx, gw)
			ctrl.postUI(func() {
				ctrl.isScanning = false
				ctrl.setActionError(err)
				ctrl.syncUI()
			}, false)
		}()
	}))
	ctrl.gpuListFrame = ctrl.cfgBox.TFrame(Style("Card.TFrame"), Padding(6))
	GridColumnConfigure(ctrl.gpuListFrame, 0, Weight(1))
}

func buildModelHubSection(ctx context.Context, ctrl *guiController, l GUILabels) {
	ctrl.lblModelHub = ctrl.cfgBox.TLabel(Style("Field.TLabel"), Txt(l.ModelHubLabel))
	ctrl.hubActions = ctrl.cfgBox.TFrame()
	ctrl.refreshModelBtn = ctrl.hubActions.TButton(Txt(l.RefreshModels), Command(func() {
		gw := strings.TrimSpace(ctrl.gwEntry.Textvariable())
		go func() {
			_, err := ctrl.eng.RefreshCatalog(ctx, gw)
			ctrl.postUI(func() {
				ctrl.setActionError(err)
				ctrl.syncUI()
			}, false)
		}()
	}))
	ctrl.applyModelBtn = ctrl.hubActions.TButton(Style("Start.TButton"), Txt(l.ApplyModels), Command(func() {
		sel := ctrl.selectedModelSlice()
		mode := ctrl.modelMode
		go func() {
			err := ctrl.eng.UpdateModelPreferences(mode, sel)
			ctrl.postUI(func() {
				ctrl.setActionError(err)
				if err == nil {
					ctrl.commitModelBaseline(mode, sel)
				}
				ctrl.syncUI()
			}, false)
		}()
	}))
	Grid(ctrl.refreshModelBtn, Row(0), Column(0), Padx("0 6"))
	Grid(ctrl.applyModelBtn, Row(0), Column(1))

	ctrl.modeRow = ctrl.cfgBox.TFrame()
	ctrl.modeAutoBtn = ctrl.modeRow.TButton(Command(func() { ctrl.setModelSelectionMode(protocol.ModelModeAuto) }))
	ctrl.modeMultiBtn = ctrl.modeRow.TButton(Command(func() { ctrl.setModelSelectionMode(protocol.ModelModeMulti) }))
	Grid(ctrl.modeAutoBtn, Row(0), Column(0), Padx("0 6"))
	Grid(ctrl.modeMultiBtn, Row(0), Column(1))

	ctrl.modelListFrame = ctrl.cfgBox.TFrame(Style("Card.TFrame"), Padding(6))
	GridColumnConfigure(ctrl.modelListFrame, 0, Weight(1))
}

func buildStartStopButton(ctrl *guiController, l GUILabels) {
	ctrl.toggleBtn = ctrl.cfgBox.TButton(Style("Start.TButton"), Txt(l.StartBtn), Command(func() {
		ctrl.handleStartStopToggle()
	}))
}

func ParseIdleSecInput(raw string) (int, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	sec, err := strconv.Atoi(trimmed)
	if err != nil || sec < worker.MinIdleThresholdSec || sec > worker.MaxIdleThresholdSec {
		return 0, false
	}
	return sec, true
}
