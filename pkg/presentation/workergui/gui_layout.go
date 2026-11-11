package workergui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	. "modernc.org/tk9.0"
)

type LayoutBand string

const (
	BandCompact         LayoutBand = "compact"
	BandStandard        LayoutBand = "standard"
	BandWide            LayoutBand = "wide"
	CanvasWheelStepPx              = 24
	resizeCoalesceDelay            = 16 * time.Millisecond
)

func DecodeTouchpadScrollDeltas(packedDelta int) (int, int) {
	u := uint32(int32(packedDelta))
	return int(int16(u >> 16)), int(int16(u & 0xffff))
}

func TouchpadScrollPixels(packedDelta int) int {
	_, deltaY := DecodeTouchpadScrollDeltas(packedDelta)
	return -deltaY
}

func MouseWheelScrollPixels(delta, fixedStep int) int {
	if fixedStep != 0 {
		return fixedStep * CanvasWheelStepPx
	}
	if delta == 0 {
		return 0
	}
	pixels := (-delta * CanvasWheelStepPx) / 120
	if pixels != 0 {
		return pixels
	}
	if delta > 0 {
		return -1
	}
	return 1
}

func NormalizeScrollbarArgs(args []string) []string {
	if len(args) != 3 || args[0] != "scroll" || args[2] != "units" {
		return args
	}
	units, err := strconv.ParseFloat(strings.TrimSpace(args[1]), 64)
	if err != nil || math.IsNaN(units) || math.IsInf(units, 0) || units == 0 {
		return args
	}
	px := int(math.Round(units * CanvasWheelStepPx))
	if px == 0 {
		if units > 0 {
			px = 1
		} else {
			px = -1
		}
	}
	return []string{"scroll", strconv.Itoa(px), "units"}
}

func ShouldShowScrollbarFractions(args []string) bool {
	if len(args) < 2 {
		return false
	}
	first, errFirst := strconv.ParseFloat(strings.TrimSpace(args[0]), 64)
	last, errLast := strconv.ParseFloat(strings.TrimSpace(args[1]), 64)
	if errFirst != nil || errLast != nil || math.IsNaN(first) || math.IsNaN(last) {
		return false
	}
	return first > 0.0005 || last < 0.9995
}

func IsWidgetOrDescendant(eventWinPath, targetWinPath string) bool {
	if eventWinPath == "" || targetWinPath == "" {
		return false
	}
	return eventWinPath == targetWinPath || strings.HasPrefix(eventWinPath, targetWinPath+".")
}

func ClassifyLayoutBand(width int) LayoutBand {
	if width < 640 {
		return BandCompact
	}
	if width < 1080 {
		return BandStandard
	}
	return BandWide
}

func ComputeWrapLength(width int) int {
	return max(240, width-48)
}

func ComputeSimpleGeometry(screenW, screenH int) string {
	targetW := 680
	targetH := 520
	if screenW > 0 && targetW > screenW-60 {
		targetW = max(380, screenW-60)
	}
	if screenH > 0 && targetH > screenH-80 {
		targetH = max(460, screenH-80)
	}
	return fmt.Sprintf("%dx%d", targetW, targetH)
}

func ComputeInitialGeometry(screenW, screenH int) string {
	targetW := 920
	targetH := 760
	if screenW >= 1600 && screenH >= 900 {
		targetW = 1140
		targetH = 760
	}
	if screenW > 0 && targetW > screenW-60 {
		targetW = max(380, screenW-60)
	}
	if screenH > 0 && targetH > screenH-80 {
		targetH = max(480, screenH-80)
	}
	return fmt.Sprintf("%dx%d", targetW, targetH)
}

func (g *guiController) bindResponsiveLayout() {
	Bind(App, "<Configure>", Command(func(e *Event) {
		if e.EventWindow != App || g.uiClosed.Load() {
			return
		}
		g.scheduleViewportResize()
	}))

	Bind(g.cfgCanvas.Window, "<Configure>", Command(func(_ *Event) {
		g.scheduleViewportResize()
	}))

	Bind(g.simpleModelCanvas.Window, "<Configure>", Command(func(_ *Event) {
		g.scheduleViewportResize()
	}))

	BindAppend("all", "<TouchpadScroll>", Command(func(e *Event) {
		g.scrollCanvasByPixels(e, TouchpadScrollPixels(e.Delta))
	}))
	BindAppend("all", "<MouseWheel>", Command(func(e *Event) {
		g.scrollCanvasByPixels(e, MouseWheelScrollPixels(e.Delta, 0))
	}))
	BindAppend("all", "<Button-4>", Command(func(e *Event) {
		g.scrollCanvasByPixels(e, MouseWheelScrollPixels(0, -1))
	}))
	BindAppend("all", "<Button-5>", Command(func(e *Event) {
		g.scrollCanvasByPixels(e, MouseWheelScrollPixels(0, 1))
	}))
}

func (g *guiController) scheduleViewportResize() {
	if g.resizePending || g.uiClosed.Load() {
		return
	}
	g.resizePending = true
	g.resizeTimerID = TclAfter(resizeCoalesceDelay, func() {
		g.resizePending = false
		g.resizeTimerID = ""
		if g.uiClosed.Load() {
			return
		}
		g.onRootResize()
	})
}

func (g *guiController) handleCanvasScrollbar(e *Event) {
	if g.uiClosed.Load() || g.cfgCanvas == nil {
		return
	}
	e.Args = NormalizeScrollbarArgs(e.Args)
	e.Yview(g.cfgCanvas)
}

func (g *guiController) handleSimpleModelScrollbar(e *Event) {
	if g.uiClosed.Load() || g.simpleModelCanvas == nil {
		return
	}
	e.Args = NormalizeScrollbarArgs(e.Args)
	e.Yview(g.simpleModelCanvas)
}

func (g *guiController) handleLogScrollSet(e *Event) {
	if g.uiClosed.Load() || g.logScroll == nil {
		return
	}
	want := ShouldShowScrollbarFractions(e.Args)
	if want != g.logScrollVisible {
		if want {
			Grid(g.logScroll, Row(0), Column(1), Sticky("ns"), Padx("4 0"))
		} else {
			GridForget(g.logScroll.Window)
		}
		g.logScrollVisible = want
	}
	if g.logScrollVisible {
		e.ScrollSet(g.logScroll)
	}
}

func (g *guiController) scrollCanvasByPixels(e *Event, pixels int) {
	if pixels == 0 || g.uiClosed.Load() || g.shouldSkipGlobalCanvasScroll(e) {
		return
	}
	target := g.activeScrollCanvas()
	if target == nil {
		return
	}
	e.Args = []string{"scroll", strconv.Itoa(pixels), "units"}
	e.Yview(target)
}

func (g *guiController) isAdvancedConfigVisible() bool {
	return g.uiMode == UIModeAdvanced && g.advTab == advTabConfig
}

func (g *guiController) shouldSkipGlobalCanvasScroll(e *Event) bool {
	if e == nil || e.EventWindow == nil {
		return false
	}
	winPath := e.EventWindow.String()
	if strings.Contains(winPath, ".popdown") {
		return true
	}
	return (g.logFrame != nil && IsWidgetOrDescendant(winPath, g.logFrame.String())) ||
		(g.cfgScroll != nil && IsWidgetOrDescendant(winPath, g.cfgScroll.String())) ||
		(g.simpleModelScroll != nil && IsWidgetOrDescendant(winPath, g.simpleModelScroll.String())) ||
		(g.langSelect != nil && IsWidgetOrDescendant(winPath, g.langSelect.String()))
}

func (g *guiController) onRootResize() {
	if g.uiClosed.Load() {
		return
	}
	w := parseScreenDim(WinfoWidth(App))
	if w >= 200 && w != g.lastRootW {
		g.lastRootW = w
		g.syncWrapLengths(w)
		band := ClassifyLayoutBand(w)
		if band != g.currentBand {
			g.applyLayoutBand(band)
			return
		}
	}
	g.syncActiveCanvasViewports()
}

func (g *guiController) syncWrapLengths(width int) {
	wrap := ComputeWrapLength(width)
	g.errBanner.Configure(Wraplength(wrap))
	g.loginSubLbl.Configure(Wraplength(max(220, wrap-48)))
	g.simpleAutoLbl.Configure(Wraplength(max(220, wrap-64)))
	g.simpleHwLbl.Configure(Wraplength(max(220, wrap-64)))
	g.simpleStatVal.Configure(Wraplength(ComputeSimpleStatusWrapLength(width)))
	if g.dbgMetaLbl != nil {
		if width < 640 {
			g.dbgMetaLbl.Configure(Wraplength(max(220, wrap-16)))
		} else {
			g.dbgMetaLbl.Configure(Wraplength(max(220, wrap-220)))
		}
	}
}

func (g *guiController) applyLayoutBand(band LayoutBand) {
	g.currentBand = band
	g.advCanvasState.contentDirty = true
	g.simpleCanvasState.contentDirty = true
	g.layoutHeader(band)
	g.layoutSimpleBars(band)
	g.layoutSimpleMetrics(band)
	g.layoutAdvancedBars(band)
	g.layoutKPICards(band)
	g.layoutFormRows(band)
	relayoutCheckFrame(g.gpuListFrame, band)
	relayoutCheckFrame(g.modelListFrame, band)
	relayoutCheckFrame(g.simpleModelListFrame, band)
	g.syncActiveCanvasViewports()
}
