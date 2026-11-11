package workergui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/presentation/workeri18n"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
	. "modernc.org/tk9.0"
)

type gpuCheckItem struct {
	deviceID string
	cb       *TCheckbuttonWidget
}

type guiController struct {
	appCtx                                                              context.Context
	eng                                                                 *worker.Engine
	lang, simpleActiveCard, modelMode, resizeTimerID, copyTimerID       string
	lastCfgSig, lastGpuSig, lastModelSig, lastLogJoined                 string
	lastSimpleModelSig, lastTitle, uiErr                                string
	lastRootW                                                           int
	uiMode                                                              UIMode
	advTab                                                              advTab
	selectedModels                                                      map[string]bool
	gpuChecks                                                           []gpuCheckItem
	synced                                                              syncedFormBaseline
	widgetCache                                                         cachedUIWidgetState
	currentBand                                                         LayoutBand
	advCanvasState, simpleCanvasState                                   canvasViewportState
	isScanning, isStartingSimple, openingBrowser, wasBrowserAuthPending bool
	logsCopiedActive, errVisible, logScrollVisible, resizePending       bool
	uiClosed                                                            atomic.Bool
	uiDone                                                              chan struct{}
	uiDoneOnce                                                          sync.Once

	hdrFrame, modeSwitchFrame, langFrame, workArea                   *TFrameWidget
	simpleRoot, simpleLoginCard, loginBtnRow, simpleHeroCard         *TFrameWidget
	simpleAcctBar, simpleAcctActions, simpleMetrics                  *TFrameWidget
	simpleCard0, simpleCard1, simpleCard2                            *TFrameWidget
	simpleInfoBox, simpleModeRow, simpleModelPane                    *TFrameWidget
	simpleModelListFrame, simpleBtnRow                               *TFrameWidget
	advRoot, advTabBar, advTabBtnFrame, advCfgPane, advDbgPane       *TFrameWidget
	dbgToolbar, dbgBtnFrame, leftPane, cfgInner                      *TFrameWidget
	schedRight, hubActions, modeRow, kpiRow                          *TFrameWidget
	gpuListFrame, modelListFrame                                     *TFrameWidget
	kpiCards                                                         [4]*TFrameWidget
	cfgShell, simpleModelShell                                       *FrameWidget
	cfgBox, logFrame                                                 *TLabelframeWidget
	cfgCanvas, simpleModelCanvas                                     *CanvasWidget
	cfgScroll, simpleModelScroll, logScroll                          *TScrollbarWidget
	langSelect                                                       *TComboboxWidget
	errBanner                                                        *LabelWidget
	logText                                                          *TextWidget
	idleCb                                                           *TCheckbuttonWidget
	gwEntry, tokEntry, nameEntry, idleSecEntry                       *TEntryWidget
	modeSimpleBtn, modeAdvBtn, loginSubmitBtn, loginCancelBtn        *TButtonWidget
	simpleWithdrawBtn, simpleSwitchBtn, simpleModeAutoBtn            *TButtonWidget
	simpleModeChooseBtn, simpleActionBtn, simpleDisconnectBtn        *TButtonWidget
	advCfgTabBtn, advDbgTabBtn, copyLogsBtn                          *TButtonWidget
	clearLogsBtn, pauseBtn, scanBtn                                  *TButtonWidget
	modeAutoBtn, modeMultiBtn, refreshModelBtn, applyModelBtn        *TButtonWidget
	toggleBtn                                                        *TButtonWidget
	titleLbl, metaLbl, langLbl, loginTitleLbl, loginSubLbl           *TLabelWidget
	simpleAcctLbl, simpleStatTitle, simpleStatVal                    *TLabelWidget
	simpleTokTitle, simpleTokVal, simpleEarnTitle, simpleEarnVal     *TLabelWidget
	simpleHwLbl, simpleHostModeLbl, simpleAutoLbl, dbgMetaLbl        *TLabelWidget
	lblGW, lblTok, lblName, lblIdleSec                               *TLabelWidget
	lblGPUs, lblModelHub, kpiTitle0, kpiTitle1, kpiTitle2, kpiTitle3 *TLabelWidget
	kpiStatus, kpiModel, kpiClearing, kpiBalance                     *TLabelWidget
}

func (g *guiController) tr(pt, en string) string {
	return localizedPair(g.lang, pt, en)
}

func (g *guiController) closeUIDone() {
	g.uiClosed.Store(true)
	g.uiDoneOnce.Do(func() {
		if g.uiDone != nil {
			close(g.uiDone)
		}
	})
}

func (g *guiController) cancelPendingTimers() {
	if g.resizeTimerID != "" {
		TclAfterCancel(g.resizeTimerID)
		g.resizeTimerID = ""
	}
	g.resizePending = false
	if g.advCanvasState.measureTimerID != "" {
		TclAfterCancel(g.advCanvasState.measureTimerID)
		g.advCanvasState.measureTimerID = ""
	}
	if g.simpleCanvasState.measureTimerID != "" {
		TclAfterCancel(g.simpleCanvasState.measureTimerID)
		g.simpleCanvasState.measureTimerID = ""
	}
	if g.copyTimerID != "" {
		TclAfterCancel(g.copyTimerID)
		g.copyTimerID = ""
	}
}

func (g *guiController) closeDesktopWindow() {
	if g.uiClosed.Load() {
		return
	}
	g.cancelPendingTimers()
	g.closeUIDone()
	Destroy(App)
}

func (g *guiController) postUI(fn func(), wait bool) {
	if g.uiClosed.Load() {
		return
	}
	PostEvent(func() {
		if g.uiClosed.Load() {
			return
		}
		fn()
	}, wait)
}

func parseScreenDim(raw string) int {
	dim, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || dim < 0 {
		return 0
	}
	return dim
}

func (g *guiController) setActionError(err error) {
	if err == nil {
		g.uiErr = ""
		return
	}
	if appErr, ok := derr.AsAppError(err); ok {
		rawMsg := strings.TrimSpace(appErr.Message)
		translated := FormatWorkerError(g.lang, rawMsg)
		if translated != rawMsg || appErr.Code == derr.CodeValidation || appErr.Code == derr.CodeIllegalState {
			g.uiErr = translated
			return
		}
		if appErr.Code == derr.CodeUpstreamFailure {
			g.uiErr = g.tr("Erro de conexão — reconectando automaticamente", "Connection error — retrying automatically")
			return
		}
		_, _, msg := workeri18n.LocalizeError(err, g.lang)
		g.uiErr = msg
		return
	}
	g.uiErr = FormatWorkerError(g.lang, err.Error())
}

func (g *guiController) applyLanguageLabels() {
	l := LabelsFor(g.lang)
	g.langLbl.Configure(Txt(l.LangLabel))
	g.langSelect.Current(LocaleIndexForCode(g.lang))
	g.lblGW.Configure(Txt(l.Gateway))
	g.lblTok.Configure(Txt(l.Token))
	g.lblName.Configure(Txt(l.Machine))
	g.cfgBox.Configure(Txt(l.BoxTitle))
	g.idleCb.Configure(Txt(l.SmartIdle))
	g.lblIdleSec.Configure(Txt(l.IdleSec))
	g.lblGPUs.Configure(Txt(l.GPUsLabel))
	if g.isScanning {
		g.scanBtn.Configure(Txt(l.Scanning))
	} else {
		g.scanBtn.Configure(Txt(l.ScanGPUs))
	}
	g.lblModelHub.Configure(Txt(l.ModelHubLabel))
	g.refreshModelBtn.Configure(Txt(l.RefreshModels))
	g.applyModelBtn.Configure(Txt(l.ApplyModels))
	g.kpiTitle0.Configure(Txt(l.KPIStatus))
	g.kpiTitle1.Configure(Txt(l.KPIModel))
	g.kpiTitle2.Configure(Txt(l.KPIClearing))
	g.kpiTitle3.Configure(Txt(l.KPIBalance))
	g.logFrame.Configure(Txt(l.LogsBox))
	g.applySimpleLabels(l)
	g.applyAdvancedLabels(l)
	g.refreshModeButtonStyles()
	g.refreshTopAndSubTabStyles()
	g.resetCachedWidgetState()
	g.lastGpuSig = ""
	g.lastModelSig = ""
	g.lastSimpleModelSig = ""
	g.lastTitle = ""
	g.advCanvasState.contentDirty = true
	g.simpleCanvasState.contentDirty = true
}

func (g *guiController) refreshModeButtonStyles() {
	l := LabelsFor(g.lang)
	isAuto := g.modelMode == protocol.ModelModeAuto
	configureActiveBtn(g.modeAutoBtn, isAuto, l.ModeAuto)
	configureActiveBtn(g.modeMultiBtn, !isAuto, l.ModeMulti)
	configureActiveBtn(g.simpleModeAutoBtn, isAuto, l.SimpleModeAutoBtn)
	configureActiveBtn(g.simpleModeChooseBtn, !isAuto, l.SimpleModeChooseBtn)
}

func BuildExternalConfigSig(s worker.StatusSnapshot) string {
	return fmt.Sprintf(
		"%v|%s|%s|%v|%v|%s|%s|%s|%s|%s|%s|%d|%s|%s|%s",
		s.Running, s.Language, s.UIMode, s.HasWorkerToken, s.BrowserAuthPending, s.WorkerToken, s.Currency,
		s.AccountEmail, s.GatewayURL, s.WorkerName,
		s.ScheduleMode, s.IdleThresholdSec,
		s.ModelMode, strings.Join(s.SelectedModels, ","), strings.Join(s.SelectedIDs, ","),
	)
}

func (g *guiController) syncExternalConfigChanges(s worker.StatusSnapshot) {
	sig := BuildExternalConfigSig(s)
	if sig == g.lastCfgSig {
		return
	}
	g.lastCfgSig = sig
	g.syncExternalAppearance(s)
	syncCheckIfClean(g.idleCb, false, boolCheckVar(s.ScheduleMode == "idle"))
	syncEntryIfClean(g.idleSecEntry, &g.synced.idleSecText, strconv.Itoa(s.IdleThresholdSec))
	if !s.Running {
		g.syncStoppedEditableEntries(s)
	}
	g.syncExternalModelSelection(s)
}

func (g *guiController) syncExternalAppearance(s worker.StatusSnapshot) {
	if (s.Language == "pt-BR" || s.Language == "en") && s.Language != g.lang {
		g.lang = s.Language
		g.applyLanguageLabels()
	}
	nextUI, err := ParseUIMode(s.UIMode)
	if err == nil && nextUI != g.uiMode {
		g.uiMode = nextUI
		g.applyUIModeTransition(nextUI)
	}
}

func (g *guiController) syncStoppedEditableEntries(s worker.StatusSnapshot) {
	syncEntryIfClean(g.gwEntry, &g.synced.gwURL, s.GatewayURL)
	syncEntryIfClean(g.tokEntry, &g.synced.workerToken, s.WorkerToken)
	syncEntryIfClean(g.nameEntry, &g.synced.workerName, s.WorkerName)
	nextGPUSig := normalizeLowerSortedSig(s.SelectedIDs)
	if !g.synced.gpusDirty && nextGPUSig != g.synced.gpusSig {
		g.synced.gpusSig = nextGPUSig
		g.lastGpuSig = ""
	}
}

func (g *guiController) syncExternalModelSelection(s worker.StatusSnapshot) {
	if g.synced.modelsDirty {
		return
	}
	nextMode := protocol.ModelModeAuto
	if s.ModelMode == protocol.ModelModeMulti {
		nextMode = protocol.ModelModeMulti
	}
	nextSig := normalizeSortedSig(s.SelectedModels)
	if nextMode == g.synced.modelMode && nextSig == g.synced.modelsSig {
		return
	}
	g.commitModelBaseline(nextMode, s.SelectedModels)
}

func (g *guiController) syncUI() {
	if g.uiClosed.Load() {
		return
	}
	s := g.eng.Snapshot()
	if g.wasBrowserAuthPending && !s.BrowserAuthPending && s.HasWorkerToken {
		WmDeiconify(App)
		Focus(Force(App))
	}
	g.wasBrowserAuthPending = s.BrowserAuthPending
	g.syncExternalConfigChanges(s)
	g.syncBanner(s)
	checkedVRAM := worker.SelectedGPUsVRAM(s.DetectedGPUs, g.effectiveCheckedGPUs(s.Running, s.SelectedIDs))
	g.syncSimpleView(s, checkedVRAM)
	g.syncRunningControls(s)
	g.syncGPUList(s.Running, s.DetectedGPUs, s.SelectedIDs)
	g.syncModelList(s, checkedVRAM)
	g.syncKPIsAndLogs(s)
}
