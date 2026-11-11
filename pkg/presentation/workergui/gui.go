package workergui

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
	. "modernc.org/tk9.0"
	_ "modernc.org/tk9.0/themes/azure"
)

func RunDesktopGUI(ctx context.Context, eng *worker.Engine, focusCh <-chan struct{}) {
	initSnap := eng.Snapshot()
	initialMode, modeErr := ParseUIMode(initSnap.UIMode)
	if modeErr != nil {
		initialMode = UIModeSimple
	}
	ctrl := &guiController{
		appCtx:         ctx,
		eng:            eng,
		uiDone:         make(chan struct{}),
		lang:           initSnap.Language,
		uiMode:         initialMode,
		advTab:         advTabConfig,
		modelMode:      initSnap.ModelMode,
		selectedModels: make(map[string]bool),
		lastCfgSig:     BuildExternalConfigSig(initSnap),
	}
	if ctrl.lang != "pt-BR" && ctrl.lang != "en" {
		ctrl.lang = "en"
	}
	if ctrl.modelMode != protocol.ModelModeMulti {
		ctrl.modelMode = protocol.ModelModeAuto
	}
	for _, m := range initSnap.SelectedModels {
		ctrl.selectedModels[m] = true
	}
	ctrl.initBaseline(initSnap)

	initBand := configureWindowAndStyles(ctrl.lang, ctrl.uiMode)
	buildHeader(ctrl)
	buildWorkspace(ctx, ctrl, initSnap)
	ctrl.applyLayoutBand(initBand)
	ctrl.bindResponsiveLayout()

	ctrl.startBackgroundLoops(ctx, ctrl.uiDone, focusCh)
	bindWindowClose(ctrl)

	ctrl.applyLanguageLabels()
	ctrl.syncUI()
	WmDeiconify(App)
	ctrl.scheduleViewportResize()
	App.Wait()
	ctrl.closeUIDone()
	eng.Stop("Window closed")
}

func configureWindowAndStyles(lang string, uiMode UIMode) LayoutBand {
	if err := ActivateTheme("azure dark"); err != nil {
		StyleThemeUse("clam")
	}
	App.WmTitle(LabelsFor(lang).WindowTitle)
	App.IconPhoto(NewPhoto(Data(buildWorkerIcon())))
	WmMinSize(App, 380, 460)
	sw := parseScreenDim(WinfoScreenWidth(App))
	sh := parseScreenDim(WinfoScreenHeight(App))
	baseGeom := ComputeSimpleGeometry(sw, sh)
	if uiMode == UIModeAdvanced {
		baseGeom = ComputeInitialGeometry(sw, sh)
	}
	WmGeometry(App, ComputeCenteredGeometry(baseGeom, sw, sh))
	App.Configure(Background("#0f1115"), Padx(12), Pady(10))
	registerThemeStyles()

	GridColumnConfigure(App, 0, Weight(1))
	GridRowConfigure(App, 2, Weight(1))
	if initW, _, ok := ParseGeometrySize(baseGeom); ok {
		return ClassifyLayoutBand(initW)
	}
	return BandStandard
}

func registerThemeStyles() {
	StyleConfigure("TFrame", Background("#0f1115"))
	StyleConfigure("Card.TFrame", Background("#161920"), Relief("solid"), Borderwidth(1))
	StyleConfigure("InnerCard.TFrame", Background("#161920"))
	StyleConfigure("SubCard.TFrame", Background("#11141a"), Relief("solid"), Borderwidth(1))
	StyleConfigure("TLabelframe", Background("#161920"))
	StyleConfigure("TLabelframe.Label", Background("#161920"), Foreground("#9ca3af"), Font("TkDefaultFont", 9, "bold"))
	StyleConfigure("Header.TLabel", Background("#0f1115"), Foreground("#f3f4f6"), Font("TkDefaultFont", 13, "bold"))
	StyleConfigure("HeroTitle.TLabel", Background("#161920"), Foreground("#f9fafb"), Font("TkDefaultFont", 13, "bold"))
	StyleConfigure("Subtle.TLabel", Background("#161920"), Foreground("#94a3b8"), Font("TkDefaultFont", 9))
	StyleConfigure("SubtleInner.TLabel", Background("#11141a"), Foreground("#94a3b8"), Font("TkDefaultFont", 9))
	StyleConfigure("Meta.TLabel", Background("#0f1115"), Foreground("#8b93a5"), Font("TkFixedFont", 9))
	StyleConfigure("Field.TLabel", Background("#161920"), Foreground("#cbd5e1"), Font("TkDefaultFont", 9))
	StyleConfigure("FieldBold.TLabel", Background("#11141a"), Foreground("#e2e8f0"), Font("TkDefaultFont", 9, "bold"))
	StyleConfigure("HeroVal.TLabel", Background("#11141a"), Foreground("#f3f4f6"), Font("TkDefaultFont", 11, "bold"))
	StyleConfigure("HeroGreen.TLabel", Background("#11141a"), Foreground("#22c55e"), Font("TkFixedFont", 13, "bold"))
	StyleConfigure("KpiTitle.TLabel", Background("#161920"), Foreground("#8b93a5"), Font("TkDefaultFont", 8, "bold"))
	StyleConfigure("KpiVal.TLabel", Background("#161920"), Foreground("#f3f4f6"), Font("TkFixedFont", 10, "bold"))
	StyleConfigure("KpiGreen.TLabel", Background("#161920"), Foreground("#22c55e"), Font("TkFixedFont", 10, "bold"))
	StyleConfigure("Gpu.TCheckbutton", Background("#161920"), Foreground("#e5e7eb"))
	StyleConfigure("Stop.TButton", Background("#dc2626"), Foreground("#ffffff"), Font("TkDefaultFont", 10, "bold"))
	StyleMap("Stop.TButton", Background, "active", "#b91c1c", "pressed", "#991b1b")
	StyleConfigure("Start.TButton", Background("#2563eb"), Foreground("#ffffff"), Font("TkDefaultFont", 9, "bold"))
	StyleMap("Start.TButton", Background, "active", "#1d4ed8", "pressed", "#1e40af")
	StyleConfigure("HeroStart.TButton", Background("#2563eb"), Foreground("#ffffff"), Font("TkDefaultFont", 11, "bold"))
	StyleMap("HeroStart.TButton", Background, "active", "#1d4ed8", "pressed", "#1e40af")
	StyleConfigure("HeroPause.TButton", Background("#d97706"), Foreground("#ffffff"), Font("TkDefaultFont", 11, "bold"))
	StyleMap("HeroPause.TButton", Background, "active", "#b45309", "pressed", "#92400e")
	StyleConfigure("HeroStop.TButton", Background("#dc2626"), Foreground("#ffffff"), Font("TkDefaultFont", 11, "bold"))
	StyleMap("HeroStop.TButton", Background, "active", "#b91c1c", "pressed", "#991b1b")
}

func buildHeader(ctrl *guiController) {
	l := LabelsFor(ctrl.lang)
	ctrl.hdrFrame = App.TFrame()
	GridColumnConfigure(ctrl.hdrFrame, 0, Weight(1))
	ctrl.titleLbl = ctrl.hdrFrame.TLabel(Style("Header.TLabel"), Txt("GPU Grid Worker"))

	ctrl.modeSwitchFrame = ctrl.hdrFrame.TFrame()
	ctrl.modeSimpleBtn = ctrl.modeSwitchFrame.TButton(Txt(l.ModeSimpleBtn), Command(func() {
		ctrl.switchUIMode(UIModeSimple)
	}))
	ctrl.modeAdvBtn = ctrl.modeSwitchFrame.TButton(Txt(l.ModeAdvancedBtn), Command(func() {
		ctrl.switchUIMode(UIModeAdvanced)
	}))
	Grid(ctrl.modeSimpleBtn, Row(0), Column(0), Padx("0 4"))
	Grid(ctrl.modeAdvBtn, Row(0), Column(1))

	ctrl.langFrame = ctrl.hdrFrame.TFrame()
	ctrl.langLbl = ctrl.langFrame.TLabel(Style("Meta.TLabel"), Txt(l.LangLabel))
	ctrl.langSelect = ctrl.langFrame.TCombobox(
		State("readonly"),
		Width(13),
		Values(LocaleOptionLabels()),
	)
	ctrl.langSelect.Current(LocaleIndexForCode(ctrl.lang))
	Bind(ctrl.langSelect.Window, "<<ComboboxSelected>>", Command(func(_ *Event) {
		ctrl.onLanguageSelected()
	}))
	Grid(ctrl.hdrFrame, Row(0), Column(0), Sticky("ew"), Pady("0 8"))

	ctrl.errBanner = App.Label(
		Background("#3b1219"),
		Foreground("#fca5a5"),
		Font("TkFixedFont", 9),
		Anchor("w"),
		Justify("left"),
		Padx(10),
		Pady(6),
		Wraplength(620),
	)
}

func (g *guiController) onLanguageSelected() {
	idx, err := strconv.Atoi(strings.TrimSpace(g.langSelect.Current(nil)))
	if err != nil {
		return
	}
	target := LocaleCodeForIndex(idx)
	if target != "pt-BR" && target != "en" {
		target = "en"
	}
	if g.lang == target {
		return
	}
	g.lang = target
	go g.eng.SetLanguage(target)
	g.applyLanguageLabels()
	g.syncUI()
}

func buildWorkspace(ctx context.Context, ctrl *guiController, s worker.StatusSnapshot) {
	ctrl.workArea = App.TFrame()
	GridColumnConfigure(ctrl.workArea, 0, Weight(1))
	GridRowConfigure(ctrl.workArea, 0, Weight(1))
	Grid(ctrl.workArea, Row(2), Column(0), Sticky("nsew"))

	buildSimpleView(ctrl, s)
	buildAdvancedView(ctx, ctrl, s)
	ctrl.layoutActiveModeView()
	ctrl.layoutAdvancedSubTab()
}

func bindWindowClose(ctrl *guiController) {
	WmProtocol(App, "WM_DELETE_WINDOW", func() {
		if ctrl.eng.Snapshot().Running {
			l := LabelsFor(ctrl.lang)
			ans := MessageBox(
				Icon("question"),
				Title(l.QuitTitle),
				Msg(l.QuitMsg),
				Detail(l.QuitDetail),
				Type("yesno"),
				Parent(App),
			)
			if ans != "yes" {
				return
			}
		}
		ctrl.closeDesktopWindow()
	})
}

func (g *guiController) startBackgroundLoops(ctx context.Context, uiDone <-chan struct{}, focusCh <-chan struct{}) {
	go g.prefetchCatalogOnStartup(ctx)
	go g.runEventSyncLoop(ctx, uiDone)
	go runFocusListener(ctx, uiDone, focusCh, g)
	go runShutdownWatcher(ctx, uiDone, g)
}

func (g *guiController) prefetchCatalogOnStartup(ctx context.Context) {
	snap := g.eng.Snapshot()
	gw := strings.TrimSpace(snap.GatewayURL)
	if gw == "" || len(snap.CatalogModels) > 0 {
		return
	}
	if _, err := g.eng.RefreshCatalog(ctx, gw); err == nil {
		g.postUI(g.syncUI, false)
	}
}

func (g *guiController) runEventSyncLoop(ctx context.Context, uiDone <-chan struct{}) {
	subCh, unsub := g.eng.Subscribe()
	defer unsub()

	safetyTicker := time.NewTicker(2 * time.Second)
	defer safetyTicker.Stop()

	scheduleSync := NewEventCoalescer(func(fn func()) {
		g.postUI(fn, false)
	}, g.syncUI)

	for {
		select {
		case <-ctx.Done():
			return
		case <-uiDone:
			return
		case _, ok := <-subCh:
			if !ok {
				return
			}
			scheduleSync()
		case <-safetyTicker.C:
			scheduleSync()
		}
	}
}

func runFocusListener(ctx context.Context, uiDone <-chan struct{}, focusCh <-chan struct{}, g *guiController) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-uiDone:
			return
		case <-focusCh:
			g.postUI(func() {
				WmDeiconify(App)
				Focus(Force(App))
			}, false)
		}
	}
}

func runShutdownWatcher(ctx context.Context, uiDone <-chan struct{}, g *guiController) {
	select {
	case <-uiDone:
	case <-ctx.Done():
		g.postUI(g.closeDesktopWindow, false)
	}
}
