package workergui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
	. "modernc.org/tk9.0"
)

const desktopTaskbarReservePx = 56

type canvasViewportState struct {
	winID            string
	measureTimerID   string
	lastAppliedW     int
	lastViewportH    int
	naturalH         int
	shellW           int
	shellH           int
	lastScrollRegion string
	contentDirty     bool
	scrollVisible    bool
	canScroll        bool
	wasScrollable    bool
}

func ParseGeometrySize(sizeSpec string) (int, int, bool) {
	trimmed := strings.TrimSpace(sizeSpec)
	if trimmed == "" {
		return 0, 0, false
	}
	if offsetIdx := strings.IndexAny(trimmed, "+-"); offsetIdx > 0 {
		trimmed = trimmed[:offsetIdx]
	}
	rawW, rawH, found := strings.Cut(trimmed, "x")
	if !found {
		return 0, 0, false
	}
	w, errW := strconv.Atoi(strings.TrimSpace(rawW))
	h, errH := strconv.Atoi(strings.TrimSpace(rawH))
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

func ComputeCenteredGeometry(sizeSpec string, screenW, screenH int) string {
	winW, winH, ok := ParseGeometrySize(sizeSpec)
	if !ok || screenW <= 0 || screenH <= 0 {
		return sizeSpec
	}
	x := max(0, (screenW-winW)/2)
	usableH := max(winH, screenH-desktopTaskbarReservePx)
	y := max(0, (usableH-winH)/2)
	if y+winH > screenH {
		y = max(0, screenH-winH)
	}
	return fmt.Sprintf("%dx%d+%d+%d", winW, winH, x, y)
}

func ComputeCanvasInnerHeight(reqHeight, viewportHeight int) int {
	cleanReq := max(0, reqHeight)
	if viewportHeight <= 0 || cleanReq > viewportHeight {
		return cleanReq
	}
	return viewportHeight
}

func ShouldShowCanvasScrollbar(naturalHeight, viewportHeight int) bool {
	return viewportHeight > 0 && naturalHeight > viewportHeight
}

func ShouldApplyViewportResize(contentDirty bool, cw, ch, lastAppliedW, lastViewportH int) bool {
	if cw < 60 || ch < 40 {
		return false
	}
	return contentDirty || cw != lastAppliedW || ch != lastViewportH
}

func FormatCanvasScrollRegion(width, innerHeight int) string {
	return fmt.Sprintf("0 0 %d %d", max(0, width), max(0, innerHeight))
}

func ChecklistColumnsForBand(band LayoutBand) int {
	if band == BandWide {
		return 2
	}
	return 1
}

func effectiveChecklistCols(band LayoutBand, count int) int {
	if count <= 1 {
		return 1
	}
	return ChecklistColumnsForBand(band)
}

func ShouldExpandForAdvanced(curW, curH int) bool {
	return curW > 0 && curH > 0 && (curW < 860 || curH < 680)
}

func ComputeSimpleStatusWrapLength(width int) int {
	wrap := ComputeWrapLength(width)
	if width < 640 {
		return max(200, wrap-60)
	}
	return max(180, (wrap-64)/2-24)
}

func (g *guiController) syncActiveCanvasViewports() {
	if g.uiClosed.Load() {
		return
	}
	if g.isAdvancedConfigVisible() {
		g.syncCanvasViewportWidth()
		return
	}
	if g.isSimpleModelCanvasVisible() {
		g.syncSimpleModelViewport()
	}
}

func (g *guiController) isSimpleModelCanvasVisible() bool {
	return g.uiMode == UIModeSimple && g.simpleActiveCard == "hero" && g.modelMode == protocol.ModelModeMulti
}

func (g *guiController) activeScrollCanvas() *CanvasWidget {
	if g.isAdvancedConfigVisible() && g.advCanvasState.canScroll {
		return g.cfgCanvas
	}
	if g.isSimpleModelCanvasVisible() && g.simpleCanvasState.canScroll {
		return g.simpleModelCanvas
	}
	return nil
}

func (g *guiController) syncCanvasViewportWidth() {
	if g.uiClosed.Load() || !g.isAdvancedConfigVisible() || g.cfgCanvas == nil || g.cfgShell == nil || g.cfgInner == nil {
		return
	}
	g.syncManagedCanvasViewport(g.cfgCanvas, g.cfgScroll, g.cfgShell, g.cfgInner.Window, &g.advCanvasState)
}

func (g *guiController) syncSimpleModelViewport() {
	if g.uiClosed.Load() || !g.isSimpleModelCanvasVisible() || g.simpleModelCanvas == nil || g.simpleModelShell == nil || g.simpleModelListFrame == nil {
		return
	}
	g.syncManagedCanvasViewport(g.simpleModelCanvas, g.simpleModelScroll, g.simpleModelShell, g.simpleModelListFrame.Window, &g.simpleCanvasState)
}

func (g *guiController) syncManagedCanvasViewport(
	canvas *CanvasWidget,
	scroll *TScrollbarWidget,
	shell *FrameWidget,
	inner *Window,
	st *canvasViewportState,
) {
	cw := parseScreenDim(WinfoWidth(canvas.Window))
	ch := parseScreenDim(WinfoHeight(canvas.Window))
	if !ShouldApplyViewportResize(st.contentDirty, cw, ch, st.lastAppliedW, st.lastViewportH) {
		return
	}
	if st.contentDirty || cw != st.lastAppliedW || st.naturalH <= 0 {
		g.scheduleNaturalHeightMeasurement(canvas, scroll, shell, inner, cw, ch, st)
		return
	}
	applyCanvasViewportHeight(canvas, scroll, shell, inner, cw, ch, st)
}

func (g *guiController) scheduleNaturalHeightMeasurement(
	canvas *CanvasWidget,
	scroll *TScrollbarWidget,
	shell *FrameWidget,
	inner *Window,
	cw int,
	ch int,
	st *canvasViewportState,
) {
	st.lastAppliedW = cw
	st.contentDirty = false
	Place(inner, X(0), Y(0), Width(cw), Height(""))
	shell.Configure(Width(cw), Height(max(st.shellH, ch)))
	if st.measureTimerID != "" {
		return
	}
	st.measureTimerID = TclAfterIdle(func() {
		st.measureTimerID = ""
		if g.uiClosed.Load() {
			return
		}
		curW := parseScreenDim(WinfoWidth(canvas.Window))
		curH := parseScreenDim(WinfoHeight(canvas.Window))
		if curW < 60 || curH < 40 {
			return
		}
		measuredH := parseScreenDim(WinfoHeight(inner))
		if measuredH > 0 {
			st.naturalH = measuredH
		}
		applyCanvasViewportHeight(canvas, scroll, shell, inner, curW, curH, st)
	})
}

func applyCanvasViewportHeight(
	canvas *CanvasWidget,
	scroll *TScrollbarWidget,
	shell *FrameWidget,
	inner *Window,
	cw int,
	ch int,
	st *canvasViewportState,
) {
	wantScroll := ShouldShowCanvasScrollbar(st.naturalH, ch)
	syncCanvasScrollbarVisibility(scroll, wantScroll, st)
	st.canScroll = wantScroll
	innerH := ComputeCanvasInnerHeight(st.naturalH, ch)
	if cw != st.shellW || innerH != st.shellH {
		Place(inner, X(0), Y(0), Width(cw), Height(innerH))
		shell.Configure(Width(cw), Height(innerH))
		st.shellW = cw
		st.shellH = innerH
	}
	region := FormatCanvasScrollRegion(cw, innerH)
	if region != st.lastScrollRegion {
		canvas.Configure(Scrollregion(region))
		st.lastScrollRegion = region
	}
	st.lastAppliedW = cw
	st.lastViewportH = ch
	if !st.canScroll && st.wasScrollable {
		resetCanvasYViewTop(canvas)
	}
	st.wasScrollable = st.canScroll
}

func syncCanvasScrollbarVisibility(scroll *TScrollbarWidget, want bool, st *canvasViewportState) {
	if scroll == nil || want == st.scrollVisible {
		return
	}
	if want {
		Grid(scroll, Row(0), Column(1), Sticky("ns"), Padx("4 0"))
	} else {
		GridForget(scroll.Window)
	}
	st.scrollVisible = want
}

func resetCanvasYViewTop(canvas *CanvasWidget) {
	ev := Event{Args: []string{"moveto", "0"}}
	ev.Yview(canvas)
}

func configureChecklistColumns(frame *TFrameWidget, cols int) {
	if frame == nil {
		return
	}
	if cols == 2 {
		GridColumnConfigure(frame, 0, Weight(1), Uniform("chk"))
		GridColumnConfigure(frame, 1, Weight(1), Uniform("chk"))
		return
	}
	GridColumnConfigure(frame, 0, Weight(1), Uniform(""))
	GridColumnConfigure(frame, 1, Weight(0), Uniform(""))
}

func gridChecklistChild(child *Window, idx, cols int) {
	if child == nil {
		return
	}
	if cols <= 1 {
		Grid(child, Row(idx), Column(0), Sticky("nw"), Padx(0), Pady(1))
		return
	}
	row := idx / cols
	col := idx % cols
	padx := "0"
	if col == 0 {
		padx = "0 8"
	}
	Grid(child, Row(row), Column(col), Sticky("nw"), Padx(padx), Pady(1))
}

func relayoutCheckFrame(frame *TFrameWidget, band LayoutBand) {
	if frame == nil {
		return
	}
	children := WinfoChildren(frame.Window)
	cols := effectiveChecklistCols(band, len(children))
	configureChecklistColumns(frame, cols)
	for idx, child := range children {
		gridChecklistChild(child, idx, cols)
	}
}
