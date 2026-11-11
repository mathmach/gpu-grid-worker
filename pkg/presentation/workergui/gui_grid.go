package workergui

import (
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
	. "modernc.org/tk9.0"
)

func (g *guiController) layoutHeader(band LayoutBand) {
	GridForget(g.titleLbl.Window, g.modeSwitchFrame.Window, g.langFrame.Window)
	GridForget(g.langLbl.Window, g.langSelect.Window)
	Grid(g.titleLbl, Row(0), Column(0), Sticky("w"))
	if band == BandCompact {
		Grid(g.langSelect, Row(0), Column(0))
		Grid(g.langFrame, Row(0), Column(1), Sticky("e"))
		Grid(g.modeSwitchFrame, Row(1), Column(0), Columnspan(2), Sticky("w"), Pady("6 0"))
		return
	}
	Grid(g.langLbl, Row(0), Column(0), Padx("0 6"))
	Grid(g.langSelect, Row(0), Column(1))
	Grid(g.modeSwitchFrame, Row(0), Column(1), Sticky("e"), Padx("0 12"))
	Grid(g.langFrame, Row(0), Column(2), Sticky("e"))
}

func (g *guiController) layoutActiveModeView() {
	GridForget(g.simpleRoot.Window, g.advRoot.Window)
	if g.uiMode == UIModeAdvanced {
		Grid(g.advRoot, Row(0), Column(0), Sticky("nsew"))
		return
	}
	Grid(g.simpleRoot, Row(0), Column(0), Sticky("nsew"))
}

func (g *guiController) layoutAdvancedSubTab() {
	GridForget(g.advCfgPane.Window, g.advDbgPane.Window)
	if g.advTab == advTabDebug {
		Grid(g.advDbgPane, Row(1), Column(0), Sticky("nsew"))
		return
	}
	Grid(g.advCfgPane, Row(1), Column(0), Sticky("nsew"))
}

func (g *guiController) layoutAdvancedBars(band LayoutBand) {
	GridForget(g.advTabBtnFrame.Window, g.metaLbl.Window)
	GridForget(g.dbgMetaLbl.Window, g.dbgBtnFrame.Window)
	if band == BandCompact {
		Grid(g.advTabBtnFrame, Row(0), Column(0), Sticky("w"))
		Grid(g.metaLbl, Row(1), Column(0), Sticky("w"), Pady("4 0"))
		Grid(g.dbgMetaLbl, Row(0), Column(0), Sticky("w"))
		Grid(g.dbgBtnFrame, Row(1), Column(0), Sticky("w"), Pady("4 0"))
		return
	}
	Grid(g.advTabBtnFrame, Row(0), Column(0), Sticky("w"))
	Grid(g.metaLbl, Row(0), Column(1), Sticky("e"), Padx("8 0"))
	Grid(g.dbgMetaLbl, Row(0), Column(0), Sticky("w"))
	Grid(g.dbgBtnFrame, Row(0), Column(1), Sticky("e"), Padx("8 0"))
}

func (g *guiController) layoutKPICards(band LayoutBand) {
	for i := range 4 {
		GridForget(g.kpiCards[i].Window)
	}
	if band != BandCompact {
		for col := range 4 {
			GridColumnConfigure(g.kpiRow, col, Weight(1), Uniform("kpi"))
		}
		Grid(g.kpiCards[0], Row(0), Column(0), Sticky("nsew"), Padx("0 6"))
		Grid(g.kpiCards[1], Row(0), Column(1), Sticky("nsew"), Padx("0 6"))
		Grid(g.kpiCards[2], Row(0), Column(2), Sticky("nsew"), Padx("0 6"))
		Grid(g.kpiCards[3], Row(0), Column(3), Sticky("nsew"))
		return
	}
	GridColumnConfigure(g.kpiRow, 0, Weight(1), Uniform("kpi"))
	GridColumnConfigure(g.kpiRow, 1, Weight(1), Uniform("kpi"))
	GridColumnConfigure(g.kpiRow, 2, Weight(0), Uniform(""))
	GridColumnConfigure(g.kpiRow, 3, Weight(0), Uniform(""))
	Grid(g.kpiCards[0], Row(0), Column(0), Sticky("nsew"), Padx("0 6"), Pady("0 6"))
	Grid(g.kpiCards[1], Row(0), Column(1), Sticky("nsew"), Pady("0 6"))
	Grid(g.kpiCards[2], Row(1), Column(0), Sticky("nsew"), Padx("0 6"))
	Grid(g.kpiCards[3], Row(1), Column(1), Sticky("nsew"))
}

func (g *guiController) layoutFormRows(band LayoutBand) {
	GridForget(
		g.lblGW.Window, g.gwEntry.Window,
		g.lblTok.Window, g.tokEntry.Window,
		g.lblName.Window, g.nameEntry.Window,
		g.idleCb.Window, g.schedRight.Window,
		g.lblGPUs.Window, g.scanBtn.Window, g.gpuListFrame.Window,
		g.lblModelHub.Window, g.hubActions.Window, g.modeRow.Window, g.modelListFrame.Window,
		g.toggleBtn.Window,
	)
	if band == BandCompact {
		g.layoutCompactForm()
	} else {
		g.layoutStandardForm()
	}
	g.widgetCache.advModelVisible = g.modelMode == protocol.ModelModeMulti
}

func (g *guiController) layoutStandardForm() {
	GridColumnConfigure(g.cfgBox, 0, Weight(0))
	GridColumnConfigure(g.cfgBox, 1, Weight(1))
	GridColumnConfigure(g.cfgBox, 2, Weight(0))
	GridRowConfigure(g.cfgBox, 14, Weight(0))
	GridRowConfigure(g.cfgBox, 8, Weight(1))

	Grid(g.lblGW, Row(0), Column(0), Sticky("w"), Padx("0 8"), Pady(2))
	Grid(g.gwEntry, Row(0), Column(1), Columnspan(2), Sticky("ew"), Pady(2))
	Grid(g.lblTok, Row(1), Column(0), Sticky("w"), Padx("0 8"), Pady(2))
	Grid(g.tokEntry, Row(1), Column(1), Columnspan(2), Sticky("ew"), Pady(2))
	Grid(g.lblName, Row(2), Column(0), Sticky("w"), Padx("0 8"), Pady(2))
	Grid(g.nameEntry, Row(2), Column(1), Columnspan(2), Sticky("ew"), Pady(2))

	Grid(g.idleCb, Row(3), Column(0), Columnspan(2), Sticky("w"), Pady(2))
	Grid(g.schedRight, Row(3), Column(2), Sticky("e"), Pady(2))

	Grid(g.lblGPUs, Row(4), Column(0), Columnspan(2), Sticky("w"), Pady("6 2"))
	Grid(g.scanBtn, Row(4), Column(2), Sticky("e"), Pady("6 2"))
	Grid(g.gpuListFrame, Row(5), Column(0), Columnspan(3), Sticky("ew"), Pady("0 6"))

	Grid(g.lblModelHub, Row(6), Column(0), Columnspan(2), Sticky("w"), Pady("4 2"))
	Grid(g.hubActions, Row(6), Column(2), Sticky("e"), Pady("4 2"))
	Grid(g.modeRow, Row(7), Column(0), Columnspan(3), Sticky("w"), Pady("2 4"))
	if g.modelMode == protocol.ModelModeMulti {
		Grid(g.modelListFrame, Row(8), Column(0), Columnspan(3), Sticky("nsew"), Pady("0 8"))
	}
	Grid(g.toggleBtn, Row(9), Column(0), Columnspan(3), Sticky("ew"), Ipady(4))
}

func (g *guiController) layoutCompactForm() {
	GridColumnConfigure(g.cfgBox, 0, Weight(1))
	GridColumnConfigure(g.cfgBox, 1, Weight(0))
	GridColumnConfigure(g.cfgBox, 2, Weight(0))
	GridRowConfigure(g.cfgBox, 8, Weight(0))
	GridRowConfigure(g.cfgBox, 14, Weight(1))

	Grid(g.lblGW, Row(0), Column(0), Sticky("w"), Pady("2 0"))
	Grid(g.gwEntry, Row(1), Column(0), Sticky("ew"), Pady("0 4"))
	Grid(g.lblTok, Row(2), Column(0), Sticky("w"), Pady("2 0"))
	Grid(g.tokEntry, Row(3), Column(0), Sticky("ew"), Pady("0 4"))
	Grid(g.lblName, Row(4), Column(0), Sticky("w"), Pady("2 0"))
	Grid(g.nameEntry, Row(5), Column(0), Sticky("ew"), Pady("0 4"))

	Grid(g.idleCb, Row(6), Column(0), Sticky("w"), Pady("2 0"))
	Grid(g.schedRight, Row(7), Column(0), Sticky("w"), Pady("2 4"))

	Grid(g.lblGPUs, Row(8), Column(0), Sticky("w"), Pady("4 2"))
	Grid(g.scanBtn, Row(9), Column(0), Sticky("ew"), Pady("0 4"))
	Grid(g.gpuListFrame, Row(10), Column(0), Sticky("ew"), Pady("0 6"))

	Grid(g.lblModelHub, Row(11), Column(0), Sticky("w"), Pady("4 2"))
	Grid(g.hubActions, Row(12), Column(0), Sticky("w"), Pady("0 4"))
	Grid(g.modeRow, Row(13), Column(0), Sticky("w"), Pady("2 4"))
	if g.modelMode == protocol.ModelModeMulti {
		Grid(g.modelListFrame, Row(14), Column(0), Sticky("nsew"), Pady("0 8"))
	}
	Grid(g.toggleBtn, Row(15), Column(0), Sticky("ew"), Ipady(4))
}

func (g *guiController) syncAdvancedModelListVisibility() {
	want := g.modelMode != protocol.ModelModeAuto
	if want == g.widgetCache.advModelVisible {
		return
	}
	g.widgetCache.advModelVisible = want
	if !want {
		GridForget(g.modelListFrame.Window)
		return
	}
	if g.currentBand == BandCompact {
		Grid(g.modelListFrame, Row(14), Column(0), Sticky("nsew"), Pady("0 8"))
		return
	}
	Grid(g.modelListFrame, Row(8), Column(0), Columnspan(3), Sticky("nsew"), Pady("0 8"))
}
