package workergui

import (
	"fmt"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
	. "modernc.org/tk9.0"
)

func (g *guiController) setModelSelectionMode(next string) {
	if g.uiMode == UIModeSimple && g.eng.Snapshot().Running {
		return
	}
	resolved := protocol.ModelModeAuto
	if next == protocol.ModelModeMulti {
		resolved = protocol.ModelModeMulti
	}
	g.modelMode = resolved
	g.synced.modelsDirty = true
	g.refreshModeButtonStyles()
	g.lastModelSig = ""
	g.lastSimpleModelSig = ""
	if resolved == protocol.ModelModeMulti {
		go g.prefetchCatalogIfEmpty()
	}
	g.syncUI()
}

func (g *guiController) prefetchCatalogIfEmpty() {
	snap := g.eng.Snapshot()
	if len(snap.CatalogModels) > 0 || strings.TrimSpace(snap.GatewayURL) == "" {
		return
	}
	if _, err := g.eng.RefreshCatalog(g.appCtx, snap.GatewayURL); err == nil {
		g.postUI(g.syncUI, false)
	}
}

func (g *guiController) handleSimpleBrowserLogin() {
	if g.openingBrowser {
		return
	}
	g.openingBrowser = true
	g.uiErr = ""
	snap := g.eng.Snapshot()
	go func() {
		var err error
		if snap.BrowserAuthPending {
			err = g.eng.ReopenBrowserLogin()
		} else {
			_, err = g.eng.StartBrowserLogin("")
		}
		g.postUI(func() {
			g.openingBrowser = false
			g.setActionError(err)
			g.syncUI()
		}, false)
	}()
}

func (g *guiController) handleSimpleCancelLogin() {
	g.uiErr = ""
	go func() {
		g.eng.CancelBrowserLogin()
		g.postUI(g.syncUI, false)
	}()
}

func (g *guiController) handleOpenWithdrawPage() {
	g.uiErr = ""
	go func() {
		err := g.eng.OpenWithdrawPage()
		g.postUI(func() {
			g.setActionError(err)
			g.syncUI()
		}, false)
	}()
}

func (g *guiController) handleSimpleSwitchAccount() {
	if g.eng.Snapshot().Running {
		return
	}
	g.uiErr = ""
	go func() {
		err := g.eng.UnlinkAccount()
		g.postUI(func() {
			g.setActionError(err)
			if err == nil {
				snap := g.eng.Snapshot()
				g.resetTokenEntryBaseline(snap.GatewayURL, snap.WorkerName)
				g.lastCfgSig = BuildExternalConfigSig(snap)
			}
			g.syncUI()
		}, false)
	}()
}

func (g *guiController) handleSimplePrimaryAction() {
	if g.isStartingSimple {
		return
	}
	snap := g.eng.Snapshot()
	if snap.Running {
		g.uiErr = ""
		if snap.ManualPaused {
			nextManual := false
			go g.eng.UpdateSchedule(snap.ScheduleMode, snap.IdleThresholdSec, &nextManual)
			return
		}
		if snap.AutoPaused {
			nextManual := false
			go g.eng.UpdateSchedule(string(nodecore.ScheduleAlways), snap.IdleThresholdSec, &nextManual)
			return
		}
		nextManual := true
		go g.eng.UpdateSchedule(snap.ScheduleMode, snap.IdleThresholdSec, &nextManual)
		return
	}
	mode := g.modelMode
	sel := g.selectedModelSlice()
	g.isStartingSimple = true
	g.uiErr = ""
	g.syncUI()
	go func() {
		err := g.eng.StartSimple(g.appCtx, mode, sel)
		g.postUI(func() {
			g.isStartingSimple = false
			g.setActionError(err)
			if err == nil {
				latest := g.eng.Snapshot()
				g.syncWidgetsFromSnapshot(latest)
			}
			g.syncUI()
		}, false)
	}()
}

func (g *guiController) handleSimpleDisconnect() {
	if !g.eng.Snapshot().Running {
		return
	}
	g.simpleDisconnectBtn.Configure(State("disabled"))
	go func() {
		g.eng.Stop("Sharing disconnected by provider.")
		g.postUI(func() {
			g.simpleDisconnectBtn.Configure(State("normal"))
			g.syncUI()
		}, false)
	}()
}

func (g *guiController) syncSimpleView(s worker.StatusSnapshot, checkedVRAM int) {
	l := LabelsFor(g.lang)
	if !s.HasWorkerToken {
		if g.simpleActiveCard != "login" {
			GridForget(g.simpleHeroCard.Window)
			Grid(g.simpleLoginCard, Row(0), Column(0), Sticky("nsew"))
			g.simpleActiveCard = "login"
		}
		g.syncSimpleLoginControls(s, l)
		return
	}
	if g.simpleActiveCard != "hero" {
		GridForget(g.simpleLoginCard.Window)
		Grid(g.simpleHeroCard, Row(0), Column(0), Sticky("nsew"))
		g.simpleActiveCard = "hero"
	}
	g.syncSimpleHeroControls(s, checkedVRAM, l)
}

func (g *guiController) syncSimpleLoginControls(s worker.StatusSnapshot, l GUILabels) {
	sig := fmt.Sprintf("%s|%v", g.lang, s.BrowserAuthPending)
	if sig == g.widgetCache.simpleLoginSig {
		return
	}
	g.widgetCache.simpleLoginSig = sig
	if s.BrowserAuthPending {
		g.loginSubLbl.Configure(Txt(l.SimpleLoginPendingSub))
		g.loginSubmitBtn.Configure(Txt(l.SimpleReopenBtn))
		if !g.widgetCache.loginCancelVisible {
			Grid(g.loginCancelBtn, Row(0), Column(1), Padx("8 0"), Ipady(10))
			g.widgetCache.loginCancelVisible = true
		}
		return
	}
	g.loginSubLbl.Configure(Txt(l.SimpleLoginSub))
	g.loginSubmitBtn.Configure(Txt(l.SimpleLoginBtn))
	if g.widgetCache.loginCancelVisible {
		GridForget(g.loginCancelBtn.Window)
		g.widgetCache.loginCancelVisible = false
	}
}

func (g *guiController) syncSimpleHeroControls(s worker.StatusSnapshot, checkedVRAM int, l GUILabels) {
	cu := s.ComputeUnitsProcessed
	if cu <= 0 && s.TokensProcessed > 0 {
		cu = s.TokensProcessed
	}
	configureLabelIfChanged(g.simpleAcctLbl, &g.widgetCache.simpleAcct, FormatAccountSummary(g.lang, s.AccountEmail, s.HasWorkerToken))
	configureLabelIfChanged(g.simpleStatVal, &g.widgetCache.simpleStat, FormatSimpleStatus(g.lang, s))
	configureLabelIfChanged(g.simpleTokVal, &g.widgetCache.simpleTok, FormatComputeUnits(g.lang, cu))
	configureLabelIfChanged(g.simpleEarnVal, &g.widgetCache.simpleEarn, FormatCreditAmount(s.Balance))
	configureLabelIfChanged(g.simpleHwLbl, &g.widgetCache.simpleHw, FormatSimpleHardwareSummary(g.lang, s.DetectedGPUs, s.SelectedIDs))

	modeBtnState := checkboxState(s.Running)
	if modeBtnState != g.widgetCache.simpleModeBtnState {
		g.widgetCache.simpleModeBtnState = modeBtnState
		g.simpleModeAutoBtn.Configure(State(modeBtnState))
		g.simpleModeChooseBtn.Configure(State(modeBtnState))
	}
	g.syncSimpleModelChecklist(s, checkedVRAM, l)
	g.syncSimpleButtonsState(s, l)
}

func (g *guiController) syncSimpleButtonsState(s worker.StatusSnapshot, l GUILabels) {
	wantDisc := s.Running
	if wantDisc != g.widgetCache.simpleDiscVisible {
		g.widgetCache.simpleDiscVisible = wantDisc
		if wantDisc {
			Grid(g.simpleDisconnectBtn, Row(0), Column(1), Padx("8 0"), Ipady(10))
		} else {
			GridForget(g.simpleDisconnectBtn.Window)
		}
	}
	switchState, actionState, actionStyle, actionTxt := resolveSimpleButtonStates(s, g.isStartingSimple, l)
	if switchState != g.widgetCache.simpleSwitchState {
		g.widgetCache.simpleSwitchState = switchState
		g.simpleSwitchBtn.Configure(State(switchState))
	}
	actionSig := actionState + "|" + actionStyle + "|" + actionTxt
	if actionSig != g.widgetCache.simpleActionSig {
		g.widgetCache.simpleActionSig = actionSig
		g.simpleActionBtn.Configure(State(actionState), Style(actionStyle), Txt(actionTxt))
	}
}

func resolveSimpleButtonStates(s worker.StatusSnapshot, starting bool, l GUILabels) (string, string, string, string) {
	if s.Running {
		if isPauseDrainingSnapshot(s) {
			return "disabled", "disabled", "HeroPause.TButton", l.SimplePausingBtn
		}
		if s.Paused || s.ManualPaused || s.AutoPaused {
			return "disabled", "normal", "HeroStart.TButton", l.SimpleResumeBtn
		}
		return "disabled", "normal", "HeroPause.TButton", l.SimplePauseBtn
	}
	if starting {
		return "disabled", "disabled", "HeroStart.TButton", l.SimpleStartingBtn
	}
	return "normal", "normal", "HeroStart.TButton", l.SimpleStartBtn
}

func (g *guiController) syncSimpleModelChecklist(s worker.StatusSnapshot, checkedVRAM int, l GUILabels) {
	isAuto := g.modelMode == protocol.ModelModeAuto
	autoTxt := l.SimpleChooseInfo
	if isAuto {
		autoTxt = l.SimpleAutoInfo
	}
	configureLabelIfChanged(g.simpleAutoLbl, &g.widgetCache.simpleAutoTxt, autoTxt)
	wantPane := !isAuto
	if wantPane != g.widgetCache.simplePaneVisible {
		g.widgetCache.simplePaneVisible = wantPane
		if wantPane {
			Grid(g.simpleModelPane, Row(3), Column(0), Sticky("nsew"), Pady("6 0"))
		} else {
			GridForget(g.simpleModelPane.Window)
		}
	}
	if isAuto {
		return
	}
	g.syncCatalogModelChecklist(s, checkedVRAM, l, catalogChecklistSpec{
		frame:        g.simpleModelListFrame,
		cbState:      checkboxState(s.Running),
		lastSig:      &g.lastSimpleModelSig,
		viewport:     &g.simpleCanvasState,
		syncViewport: g.syncSimpleModelViewport,
	})
}
