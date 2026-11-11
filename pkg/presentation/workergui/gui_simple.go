package workergui

import (
	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
	. "modernc.org/tk9.0"
)

func buildSimpleView(ctrl *guiController, _ worker.StatusSnapshot) {
	ctrl.simpleRoot = ctrl.workArea.TFrame()
	GridColumnConfigure(ctrl.simpleRoot, 0, Weight(1))
	GridRowConfigure(ctrl.simpleRoot, 0, Weight(1))

	buildSimpleLoginCard(ctrl)
	buildSimpleHeroCard(ctrl)
}

func buildSimpleLoginCard(ctrl *guiController) {
	l := LabelsFor(ctrl.lang)
	card := ctrl.simpleRoot.TFrame(Style("Card.TFrame"), Padding(24))
	GridColumnConfigure(card, 0, Weight(1))
	GridRowConfigure(card, 0, Weight(1))
	ctrl.simpleLoginCard = card

	body := card.TFrame(Style("InnerCard.TFrame"))
	GridColumnConfigure(body, 0, Weight(1))

	ctrl.loginTitleLbl = body.TLabel(Style("HeroTitle.TLabel"), Txt(l.SimpleLoginTitle))
	ctrl.loginSubLbl = body.TLabel(Style("Subtle.TLabel"), Txt(l.SimpleLoginSub), Wraplength(560), Justify("left"))

	ctrl.loginBtnRow = body.TFrame(Style("InnerCard.TFrame"))
	GridColumnConfigure(ctrl.loginBtnRow, 0, Weight(1))
	ctrl.loginSubmitBtn = ctrl.loginBtnRow.TButton(Style("HeroStart.TButton"), Txt(l.SimpleLoginBtn), Command(func() {
		ctrl.handleSimpleBrowserLogin()
	}))
	ctrl.loginCancelBtn = ctrl.loginBtnRow.TButton(Txt(l.SimpleCancelLoginBtn), Command(func() {
		ctrl.handleSimpleCancelLogin()
	}))
	Grid(ctrl.loginSubmitBtn, Row(0), Column(0), Sticky("ew"), Ipady(10))

	Grid(ctrl.loginTitleLbl, Row(0), Column(0), Sticky("w"), Pady("0 6"))
	Grid(ctrl.loginSubLbl, Row(1), Column(0), Sticky("w"), Pady("0 18"))
	Grid(ctrl.loginBtnRow, Row(2), Column(0), Sticky("ew"))
	Grid(body, Row(0), Column(0), Sticky("ew"))
}

func buildSimpleHeroCard(ctrl *guiController) {
	l := LabelsFor(ctrl.lang)
	hero := ctrl.simpleRoot.TFrame(Style("Card.TFrame"), Padding(18))
	GridColumnConfigure(hero, 0, Weight(1))
	GridRowConfigure(hero, 2, Weight(1))
	ctrl.simpleHeroCard = hero

	ctrl.simpleAcctBar = hero.TFrame(Style("InnerCard.TFrame"))
	GridColumnConfigure(ctrl.simpleAcctBar, 0, Weight(1))
	ctrl.simpleAcctLbl = ctrl.simpleAcctBar.TLabel(Style("Field.TLabel"), Txt("-"))
	ctrl.simpleAcctActions = ctrl.simpleAcctBar.TFrame(Style("InnerCard.TFrame"))
	ctrl.simpleWithdrawBtn = ctrl.simpleAcctActions.TButton(Style("Start.TButton"), Txt(l.SimpleWithdrawBtn), Command(func() {
		ctrl.handleOpenWithdrawPage()
	}))
	ctrl.simpleSwitchBtn = ctrl.simpleAcctActions.TButton(Txt(l.SimpleSwitchAcct), Command(func() {
		ctrl.handleSimpleSwitchAccount()
	}))
	Grid(ctrl.simpleWithdrawBtn, Row(0), Column(0), Padx("0 6"))
	Grid(ctrl.simpleSwitchBtn, Row(0), Column(1))
	Grid(ctrl.simpleAcctBar, Row(0), Column(0), Sticky("ew"), Pady("0 12"))

	buildSimpleMetricsRow(ctrl, hero, l)
	buildSimpleHostingBox(ctrl, hero, l)
	buildSimpleActionRow(ctrl, hero, l)
}

func buildSimpleMetricsRow(ctrl *guiController, parent *TFrameWidget, l GUILabels) {
	ctrl.simpleMetrics = parent.TFrame(Style("InnerCard.TFrame"))

	ctrl.simpleCard0 = ctrl.simpleMetrics.TFrame(Style("SubCard.TFrame"), Padding(10))
	GridColumnConfigure(ctrl.simpleCard0, 0, Weight(1))
	ctrl.simpleStatTitle = ctrl.simpleCard0.TLabel(Style("KpiTitle.TLabel"), Txt(l.SimpleStatusTitle))
	ctrl.simpleStatVal = ctrl.simpleCard0.TLabel(Style("HeroVal.TLabel"), Txt("-"), Wraplength(200), Justify("left"))
	Grid(ctrl.simpleStatTitle, Row(0), Column(0), Sticky("w"))
	Grid(ctrl.simpleStatVal, Row(1), Column(0), Sticky("w"), Pady("4 0"))

	ctrl.simpleCard1 = ctrl.simpleMetrics.TFrame(Style("SubCard.TFrame"), Padding(10))
	GridColumnConfigure(ctrl.simpleCard1, 0, Weight(1))
	ctrl.simpleTokTitle = ctrl.simpleCard1.TLabel(Style("KpiTitle.TLabel"), Txt(l.SimpleTokensTitle))
	ctrl.simpleTokVal = ctrl.simpleCard1.TLabel(Style("HeroVal.TLabel"), Txt("0"))
	Grid(ctrl.simpleTokTitle, Row(0), Column(0), Sticky("w"))
	Grid(ctrl.simpleTokVal, Row(1), Column(0), Sticky("w"), Pady("4 0"))

	ctrl.simpleCard2 = ctrl.simpleMetrics.TFrame(Style("SubCard.TFrame"), Padding(10))
	GridColumnConfigure(ctrl.simpleCard2, 0, Weight(1))
	ctrl.simpleEarnTitle = ctrl.simpleCard2.TLabel(Style("KpiTitle.TLabel"), Txt(l.SimpleEarnedTitle))
	ctrl.simpleEarnVal = ctrl.simpleCard2.TLabel(Style("HeroGreen.TLabel"), Txt("-"))
	Grid(ctrl.simpleEarnTitle, Row(0), Column(0), Sticky("w"))
	Grid(ctrl.simpleEarnVal, Row(1), Column(0), Sticky("w"), Pady("4 0"))

	Grid(ctrl.simpleMetrics, Row(1), Column(0), Sticky("ew"), Pady("0 12"))
}

func buildSimpleHostingBox(ctrl *guiController, parent *TFrameWidget, l GUILabels) {
	ctrl.simpleInfoBox = parent.TFrame(Style("SubCard.TFrame"), Padding(12))
	GridColumnConfigure(ctrl.simpleInfoBox, 0, Weight(1))
	GridRowConfigure(ctrl.simpleInfoBox, 3, Weight(1))

	ctrl.simpleHwLbl = ctrl.simpleInfoBox.TLabel(Style("FieldBold.TLabel"), Txt("-"), Justify("left"))
	ctrl.simpleModeRow = ctrl.simpleInfoBox.TFrame(Style("SubCard.TFrame"))
	ctrl.simpleHostModeLbl = ctrl.simpleModeRow.TLabel(Style("SubtleInner.TLabel"), Txt(l.SimpleHostModeLbl))
	ctrl.simpleModeAutoBtn = ctrl.simpleModeRow.TButton(Txt(l.SimpleModeAutoBtn), Command(func() {
		ctrl.setModelSelectionMode(protocol.ModelModeAuto)
	}))
	ctrl.simpleModeChooseBtn = ctrl.simpleModeRow.TButton(Txt(l.SimpleModeChooseBtn), Command(func() {
		ctrl.setModelSelectionMode(protocol.ModelModeMulti)
	}))

	ctrl.simpleAutoLbl = ctrl.simpleInfoBox.TLabel(Style("SubtleInner.TLabel"), Txt(l.SimpleAutoInfo), Wraplength(560), Justify("left"))

	ctrl.simpleModelPane = ctrl.simpleInfoBox.TFrame(Style("SubCard.TFrame"))
	GridColumnConfigure(ctrl.simpleModelPane, 0, Weight(1))
	GridRowConfigure(ctrl.simpleModelPane, 0, Weight(1))
	ctrl.simpleModelCanvas = ctrl.simpleModelPane.Canvas(
		Background("#161920"),
		Borderwidth(0),
		Highlightthickness(0),
		Height(96),
		Yscrollincrement(1),
		Yscrollcommand(func(e *Event) { e.ScrollSet(ctrl.simpleModelScroll) }),
	)
	ctrl.simpleModelScroll = ctrl.simpleModelPane.TScrollbar(Command(func(e *Event) {
		ctrl.handleSimpleModelScrollbar(e)
	}))
	Grid(ctrl.simpleModelCanvas, Row(0), Column(0), Sticky("nsew"))

	ctrl.simpleModelShell = ctrl.simpleModelCanvas.Frame(
		Background("#161920"),
		Borderwidth(0),
		Highlightthickness(0),
	)
	ctrl.simpleModelListFrame = ctrl.simpleModelShell.TFrame(Style("Card.TFrame"), Padding(6))
	GridColumnConfigure(ctrl.simpleModelListFrame, 0, Weight(1))
	Place(ctrl.simpleModelListFrame.Window, X(0), Y(0))
	ctrl.simpleCanvasState.winID = ctrl.simpleModelCanvas.CreateWindow(0, 0, Anchor("nw"), ItemWindow(ctrl.simpleModelShell.Window))
	ctrl.simpleCanvasState.contentDirty = true

	Grid(ctrl.simpleHwLbl, Row(0), Column(0), Sticky("w"), Pady("0 6"))
	Grid(ctrl.simpleModeRow, Row(1), Column(0), Sticky("w"), Pady("0 6"))
	Grid(ctrl.simpleAutoLbl, Row(2), Column(0), Sticky("nw"))
	Grid(ctrl.simpleInfoBox, Row(2), Column(0), Sticky("nsew"), Pady("0 14"))
}

func buildSimpleActionRow(ctrl *guiController, parent *TFrameWidget, l GUILabels) {
	ctrl.simpleBtnRow = parent.TFrame(Style("InnerCard.TFrame"))
	GridColumnConfigure(ctrl.simpleBtnRow, 0, Weight(1))
	ctrl.simpleActionBtn = ctrl.simpleBtnRow.TButton(Style("HeroStart.TButton"), Txt(l.SimpleStartBtn), Command(func() {
		ctrl.handleSimplePrimaryAction()
	}))
	ctrl.simpleDisconnectBtn = ctrl.simpleBtnRow.TButton(Style("Stop.TButton"), Txt(l.SimpleDisconnectBtn), Command(func() {
		ctrl.handleSimpleDisconnect()
	}))
	Grid(ctrl.simpleActionBtn, Row(0), Column(0), Sticky("ew"), Ipady(10))
	Grid(ctrl.simpleBtnRow, Row(3), Column(0), Sticky("ew"))
}

func (g *guiController) layoutSimpleBars(band LayoutBand) {
	GridForget(g.simpleAcctLbl.Window, g.simpleAcctActions.Window)
	GridForget(g.simpleHostModeLbl.Window, g.simpleModeAutoBtn.Window, g.simpleModeChooseBtn.Window)
	if band == BandCompact {
		Grid(g.simpleAcctLbl, Row(0), Column(0), Sticky("w"))
		Grid(g.simpleAcctActions, Row(1), Column(0), Sticky("w"), Pady("6 0"))
		Grid(g.simpleHostModeLbl, Row(0), Column(0), Columnspan(2), Sticky("w"), Pady("0 4"))
		Grid(g.simpleModeAutoBtn, Row(1), Column(0), Sticky("w"), Padx("0 6"))
		Grid(g.simpleModeChooseBtn, Row(1), Column(1), Sticky("w"))
		return
	}
	Grid(g.simpleAcctLbl, Row(0), Column(0), Sticky("w"))
	Grid(g.simpleAcctActions, Row(0), Column(1), Sticky("e"), Padx("8 0"))
	Grid(g.simpleHostModeLbl, Row(0), Column(0), Padx("0 8"))
	Grid(g.simpleModeAutoBtn, Row(0), Column(1), Padx("0 6"))
	Grid(g.simpleModeChooseBtn, Row(0), Column(2))
}

func (g *guiController) layoutSimpleMetrics(band LayoutBand) {
	GridForget(g.simpleCard0.Window, g.simpleCard1.Window, g.simpleCard2.Window)
	if band == BandCompact {
		for col := range 3 {
			w := 0
			if col == 0 {
				w = 1
			}
			GridColumnConfigure(g.simpleMetrics, col, Weight(w), Uniform(""))
		}
		Grid(g.simpleCard0, Row(0), Column(0), Sticky("nsew"), Pady("0 6"))
		Grid(g.simpleCard1, Row(1), Column(0), Sticky("nsew"), Pady("0 6"))
		Grid(g.simpleCard2, Row(2), Column(0), Sticky("nsew"))
		return
	}
	GridColumnConfigure(g.simpleMetrics, 0, Weight(2), Uniform("smet"))
	GridColumnConfigure(g.simpleMetrics, 1, Weight(1), Uniform("smet"))
	GridColumnConfigure(g.simpleMetrics, 2, Weight(1), Uniform("smet"))
	Grid(g.simpleCard0, Row(0), Column(0), Sticky("nsew"), Padx("0 8"))
	Grid(g.simpleCard1, Row(0), Column(1), Sticky("nsew"), Padx("0 8"))
	Grid(g.simpleCard2, Row(0), Column(2), Sticky("nsew"))
}

func (g *guiController) applySimpleLabels(l GUILabels) {
	g.loginTitleLbl.Configure(Txt(l.SimpleLoginTitle))
	g.loginCancelBtn.Configure(Txt(l.SimpleCancelLoginBtn))
	g.simpleWithdrawBtn.Configure(Txt(l.SimpleWithdrawBtn))
	g.simpleSwitchBtn.Configure(Txt(l.SimpleSwitchAcct))
	g.simpleStatTitle.Configure(Txt(l.SimpleStatusTitle))
	g.simpleTokTitle.Configure(Txt(l.SimpleTokensTitle))
	g.simpleEarnTitle.Configure(Txt(l.SimpleEarnedTitle))
	g.simpleHostModeLbl.Configure(Txt(l.SimpleHostModeLbl))
	g.simpleDisconnectBtn.Configure(Txt(l.SimpleDisconnectBtn))
}
