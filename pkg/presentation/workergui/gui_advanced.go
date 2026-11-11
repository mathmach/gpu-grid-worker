package workergui

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	. "modernc.org/tk9.0"
)

func buildAdvancedView(ctx context.Context, ctrl *guiController, s worker.StatusSnapshot) {
	ctrl.advRoot = ctrl.workArea.TFrame()
	GridColumnConfigure(ctrl.advRoot, 0, Weight(1))
	GridRowConfigure(ctrl.advRoot, 1, Weight(1))

	buildAdvancedTabBar(ctrl, s)
	buildAdvancedConfigPane(ctx, ctrl, s)
	buildAdvancedDebugPane(ctrl)
}

func buildAdvancedTabBar(ctrl *guiController, s worker.StatusSnapshot) {
	l := LabelsFor(ctrl.lang)
	ctrl.advTabBar = ctrl.advRoot.TFrame()
	GridColumnConfigure(ctrl.advTabBar, 0, Weight(1))

	ctrl.advTabBtnFrame = ctrl.advTabBar.TFrame()
	ctrl.advCfgTabBtn = ctrl.advTabBtnFrame.TButton(Txt(l.AdvTabSettings), Command(func() {
		ctrl.switchAdvTab(advTabConfig)
	}))
	ctrl.advDbgTabBtn = ctrl.advTabBtnFrame.TButton(Txt(l.AdvTabDebug), Command(func() {
		ctrl.switchAdvTab(advTabDebug)
	}))
	Grid(ctrl.advCfgTabBtn, Row(0), Column(0), Padx("0 6"))
	Grid(ctrl.advDbgTabBtn, Row(0), Column(1))

	metaTxt := fmt.Sprintf("%s/%s  |  %s  |  PID: %d", runtime.GOOS, runtime.GOARCH, s.MachineID, os.Getpid())
	ctrl.metaLbl = ctrl.advTabBar.TLabel(Style("Meta.TLabel"), Txt(metaTxt))

	Grid(ctrl.advTabBar, Row(0), Column(0), Sticky("ew"), Pady("0 8"))
}

func buildAdvancedConfigPane(ctx context.Context, ctrl *guiController, s worker.StatusSnapshot) {
	ctrl.advCfgPane = ctrl.advRoot.TFrame()
	GridColumnConfigure(ctrl.advCfgPane, 0, Weight(1))
	GridRowConfigure(ctrl.advCfgPane, 1, Weight(1))

	buildKPICards(ctrl)

	ctrl.leftPane = ctrl.advCfgPane.TFrame()
	GridColumnConfigure(ctrl.leftPane, 0, Weight(1))
	GridRowConfigure(ctrl.leftPane, 0, Weight(1))

	ctrl.cfgCanvas = ctrl.leftPane.Canvas(
		Background("#0f1115"),
		Borderwidth(0),
		Highlightthickness(0),
		Height(160),
		Yscrollincrement(1),
		Yscrollcommand(func(e *Event) { e.ScrollSet(ctrl.cfgScroll) }),
	)
	ctrl.cfgScroll = ctrl.leftPane.TScrollbar(Command(func(e *Event) { ctrl.handleCanvasScrollbar(e) }))
	Grid(ctrl.cfgCanvas, Row(0), Column(0), Sticky("nsew"))

	ctrl.cfgShell = ctrl.cfgCanvas.Frame(
		Background("#0f1115"),
		Borderwidth(0),
		Highlightthickness(0),
	)
	ctrl.cfgInner = ctrl.cfgShell.TFrame()
	GridColumnConfigure(ctrl.cfgInner, 0, Weight(1))
	GridRowConfigure(ctrl.cfgInner, 0, Weight(1))
	Place(ctrl.cfgInner.Window, X(0), Y(0))
	ctrl.advCanvasState.winID = ctrl.cfgCanvas.CreateWindow(0, 0, Anchor("nw"), ItemWindow(ctrl.cfgShell.Window))
	ctrl.advCanvasState.contentDirty = true

	l := LabelsFor(ctrl.lang)
	ctrl.cfgBox = ctrl.cfgInner.TLabelframe(Txt(l.BoxTitle), Padding(10))
	Grid(ctrl.cfgBox, Row(0), Column(0), Sticky("nsew"))

	buildConnectionRows(ctrl, s)
	buildGPUSection(ctx, ctrl, l)
	buildModelHubSection(ctx, ctrl, l)
	buildStartStopButton(ctrl, l)

	Grid(ctrl.leftPane, Row(1), Column(0), Sticky("nsew"))
}

func buildKPICards(ctrl *guiController) {
	ctrl.kpiRow = ctrl.advCfgPane.TFrame()
	for idx := range 4 {
		valStyle := "KpiVal.TLabel"
		if idx == 3 {
			valStyle = "KpiGreen.TLabel"
		}
		t, v := buildSingleKPICard(ctrl, idx, valStyle)
		assignKPILabels(ctrl, idx, t, v)
	}
	Grid(ctrl.kpiRow, Row(0), Column(0), Sticky("ew"), Pady("0 8"))
}

func buildSingleKPICard(ctrl *guiController, idx int, valStyle string) (*TLabelWidget, *TLabelWidget) {
	card := ctrl.kpiRow.TFrame(Style("Card.TFrame"), Padding(8))
	GridColumnConfigure(card, 0, Weight(1))
	t := card.TLabel(Style("KpiTitle.TLabel"), Txt("-"))
	v := card.TLabel(Style(valStyle), Txt("-"))
	Grid(t, Row(0), Column(0), Sticky("w"))
	Grid(v, Row(1), Column(0), Sticky("w"), Pady("2 0"))
	ctrl.kpiCards[idx] = card
	return t, v
}

func assignKPILabels(ctrl *guiController, idx int, t, v *TLabelWidget) {
	switch idx {
	case 0:
		ctrl.kpiTitle0, ctrl.kpiStatus = t, v
	case 1:
		ctrl.kpiTitle1, ctrl.kpiModel = t, v
	case 2:
		ctrl.kpiTitle2, ctrl.kpiClearing = t, v
	case 3:
		ctrl.kpiTitle3, ctrl.kpiBalance = t, v
	}
}

func buildAdvancedDebugPane(ctrl *guiController) {
	l := LabelsFor(ctrl.lang)
	ctrl.advDbgPane = ctrl.advRoot.TFrame()
	GridColumnConfigure(ctrl.advDbgPane, 0, Weight(1))
	GridRowConfigure(ctrl.advDbgPane, 1, Weight(1))

	ctrl.dbgToolbar = ctrl.advDbgPane.TFrame()
	GridColumnConfigure(ctrl.dbgToolbar, 0, Weight(1))
	ctrl.dbgMetaLbl = ctrl.dbgToolbar.TLabel(Style("Meta.TLabel"), Txt("-"))
	ctrl.dbgBtnFrame = ctrl.dbgToolbar.TFrame()
	ctrl.copyLogsBtn = ctrl.dbgBtnFrame.TButton(Txt(l.CopyLogsBtn), Command(func() {
		ctrl.handleCopyLogs()
	}))
	ctrl.clearLogsBtn = ctrl.dbgBtnFrame.TButton(Txt(l.ClearLogsBtn), Command(func() {
		ctrl.handleClearLogs()
	}))
	Grid(ctrl.copyLogsBtn, Row(0), Column(0), Padx("0 6"))
	Grid(ctrl.clearLogsBtn, Row(0), Column(1))
	Grid(ctrl.dbgToolbar, Row(0), Column(0), Sticky("ew"), Pady("0 6"))

	ctrl.logFrame = ctrl.advDbgPane.TLabelframe(Padding(6))
	GridColumnConfigure(ctrl.logFrame, 0, Weight(1))
	GridRowConfigure(ctrl.logFrame, 0, Weight(1))

	ctrl.logText = ctrl.logFrame.Text(
		Height(14),
		Wrap("word"),
		Background("#0b0d11"),
		Foreground("#a5aec0"),
		Font("TkFixedFont", 9),
		Borderwidth(0),
		Highlightthickness(0),
		Padx(8),
		Pady(6),
		State("disabled"),
		Yscrollcommand(func(e *Event) { ctrl.handleLogScrollSet(e) }),
	)
	ctrl.logScroll = ctrl.logFrame.TScrollbar(Command(func(e *Event) { e.Yview(ctrl.logText) }))
	Grid(ctrl.logText, Row(0), Column(0), Sticky("nsew"))
	Grid(ctrl.logFrame, Row(1), Column(0), Sticky("nsew"))
}

func (g *guiController) switchUIMode(next UIMode) {
	resolved, err := ParseUIMode(string(next))
	if err != nil || g.uiMode == resolved {
		return
	}
	g.uiMode = resolved
	go g.eng.SetUIMode(string(resolved))
	g.applyUIModeTransition(resolved)
	g.syncUI()
}

func (g *guiController) applyUIModeTransition(resolved UIMode) {
	g.refreshTopAndSubTabStyles()
	g.layoutActiveModeView()
	if resolved == UIModeAdvanced {
		g.expandWindowForAdvanced()
	}
	g.advCanvasState.contentDirty = true
	g.simpleCanvasState.contentDirty = true
	g.lastRootW = 0
	g.scheduleViewportResize()
}

func (g *guiController) expandWindowForAdvanced() {
	curW := parseScreenDim(WinfoWidth(App))
	curH := parseScreenDim(WinfoHeight(App))
	if !ShouldExpandForAdvanced(curW, curH) {
		return
	}
	sw := parseScreenDim(WinfoScreenWidth(App))
	sh := parseScreenDim(WinfoScreenHeight(App))
	WmGeometry(App, ComputeCenteredGeometry(ComputeInitialGeometry(sw, sh), sw, sh))
}

func (g *guiController) switchAdvTab(next advTab) {
	if g.advTab == next {
		return
	}
	g.advTab = next
	g.refreshTopAndSubTabStyles()
	g.layoutAdvancedSubTab()
	if next == advTabConfig {
		g.advCanvasState.contentDirty = true
		g.scheduleViewportResize()
	}
	g.syncUI()
}

func (g *guiController) refreshTopAndSubTabStyles() {
	l := LabelsFor(g.lang)
	configureActiveBtn(g.modeSimpleBtn, g.uiMode == UIModeSimple, l.ModeSimpleBtn)
	configureActiveBtn(g.modeAdvBtn, g.uiMode == UIModeAdvanced, l.ModeAdvancedBtn)
	configureActiveBtn(g.advCfgTabBtn, g.advTab == advTabConfig, l.AdvTabSettings)
	configureActiveBtn(g.advDbgTabBtn, g.advTab == advTabDebug, l.AdvTabDebug)
}

func configureActiveBtn(btn *TButtonWidget, active bool, label string) {
	if btn == nil {
		return
	}
	if active {
		btn.Configure(Style("Start.TButton"), Txt(label))
		return
	}
	btn.Configure(Style("TButton"), Txt(label))
}

func (g *guiController) handleCopyLogs() {
	raw := strings.Join(g.eng.Snapshot().Logs, "\n")
	ClipboardClear()
	if raw != "" {
		ClipboardAppend(raw)
	}
	g.logsCopiedActive = true
	g.copyLogsBtn.Configure(Txt(LabelsFor(g.lang).LogsCopiedBtn))
	if g.copyTimerID != "" {
		TclAfterCancel(g.copyTimerID)
	}
	g.copyTimerID = TclAfter(1500*time.Millisecond, func() {
		g.copyTimerID = ""
		g.logsCopiedActive = false
		if !g.uiClosed.Load() && g.copyLogsBtn != nil {
			g.copyLogsBtn.Configure(Txt(LabelsFor(g.lang).CopyLogsBtn))
		}
	})
}

func (g *guiController) handleClearLogs() {
	go func() {
		g.eng.ClearLogs()
		g.postUI(g.syncUI, false)
	}()
}

func (g *guiController) applyAdvancedLabels(l GUILabels) {
	if g.logsCopiedActive {
		g.copyLogsBtn.Configure(Txt(l.LogsCopiedBtn))
	} else {
		g.copyLogsBtn.Configure(Txt(l.CopyLogsBtn))
	}
	g.clearLogsBtn.Configure(Txt(l.ClearLogsBtn))
}
