package workergui_test

import (
	"strings"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/presentation/workergui"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func TestComputeCenteredGeometryKeepsInitialWindowFullyOnScreen(t *testing.T) {
	simple1080 := workergui.ComputeCenteredGeometry("680x520", 1920, 1080)
	if simple1080 != "680x520+620+252" {
		t.Fatalf("expected centered simple geometry 680x520+620+252 on 1080p, got %s", simple1080)
	}

	adv1080 := workergui.ComputeCenteredGeometry("1140x760", 1920, 1080)
	if adv1080 != "1140x760+390+132" {
		t.Fatalf("expected centered advanced geometry 1140x760+390+132 on 1080p, got %s", adv1080)
	}

	adv768 := workergui.ComputeCenteredGeometry("920x688", 1366, 768)
	if adv768 != "920x688+223+12" {
		t.Fatalf("expected taskbar-safe centered geometry 920x688+223+12 on 768p, got %s", adv768)
	}

	tight480 := workergui.ComputeCenteredGeometry("580x460", 640, 480)
	if tight480 != "580x460+30+0" {
		t.Fatalf("expected clamped centered geometry 580x460+30+0 on 640x480, got %s", tight480)
	}

	unknownScreen := workergui.ComputeCenteredGeometry("680x520", 0, 0)
	if unknownScreen != "680x520" {
		t.Fatalf("expected size-only geometry when screen dimensions unavailable, got %s", unknownScreen)
	}
}

func TestParseGeometrySizeHandlesPlainAndOffsetSpecs(t *testing.T) {
	w, h, ok := workergui.ParseGeometrySize("1140x760+390+132")
	if !ok || w != 1140 || h != 760 {
		t.Fatalf("expected 1140x760 ok=true, got %dx%d ok=%v", w, h, ok)
	}

	w2, h2, ok2 := workergui.ParseGeometrySize("680x520")
	if !ok2 || w2 != 680 || h2 != 520 {
		t.Fatalf("expected 680x520 ok=true, got %dx%d ok=%v", w2, h2, ok2)
	}

	if _, _, bad := workergui.ParseGeometrySize("invalid"); bad {
		t.Fatal("expected malformed geometry spec to return false")
	}
}

func TestComputeCanvasInnerHeightExpandsToFullViewportOrContentOverflow(t *testing.T) {
	if got := workergui.ComputeCanvasInnerHeight(320, 880); got != 880 {
		t.Fatalf("expected short content to expand to 880px full-screen viewport, got %d", got)
	}
	if got := workergui.ComputeCanvasInnerHeight(960, 540); got != 960 {
		t.Fatalf("expected overflowing content height 960px to be preserved for scrolling, got %d", got)
	}
	if got := workergui.ComputeCanvasInnerHeight(410, 0); got != 410 {
		t.Fatalf("expected fallback to requested height 410px when viewport is zero, got %d", got)
	}
}

func TestWideScreenChecklistColumnsAndModeExpansionGuard(t *testing.T) {
	if cols := workergui.ChecklistColumnsForBand(workergui.BandCompact); cols != 1 {
		t.Fatalf("expected 1 column in compact band, got %d", cols)
	}
	if cols := workergui.ChecklistColumnsForBand(workergui.BandStandard); cols != 1 {
		t.Fatalf("expected 1 column in standard band, got %d", cols)
	}
	if cols := workergui.ChecklistColumnsForBand(workergui.BandWide); cols != 2 {
		t.Fatalf("expected 2 columns in wide band for 100%% screen utilization, got %d", cols)
	}

	if !workergui.ShouldExpandForAdvanced(680, 520) {
		t.Fatal("expected small simple window 680x520 to expand when entering advanced mode")
	}
	if workergui.ShouldExpandForAdvanced(1920, 1017) {
		t.Fatal("expected maximized 1920x1017 window not to be resized when switching modes")
	}
	if workergui.ShouldExpandForAdvanced(0, 0) {
		t.Fatal("expected unmapped 0x0 window not to trigger resize override")
	}

	if wrapCompact := workergui.ComputeSimpleStatusWrapLength(480); wrapCompact != 372 {
		t.Fatalf("expected compact status wrap 372, got %d", wrapCompact)
	}
	if wrapFullHD := workergui.ComputeSimpleStatusWrapLength(1920); wrapFullHD != 880 {
		t.Fatalf("expected 1080p half-row status wrap 880, got %d", wrapFullHD)
	}
}

func TestAutoHideScrollbarPredicatesPreventUnnecessaryScroll(t *testing.T) {
	if workergui.ShouldShowCanvasScrollbar(360, 600) {
		t.Fatal("expected scrollbar hidden when natural height 360 fits inside 600px viewport")
	}
	if workergui.ShouldShowCanvasScrollbar(600, 600) {
		t.Fatal("expected scrollbar hidden when natural height equals 600px viewport")
	}
	if !workergui.ShouldShowCanvasScrollbar(640, 600) {
		t.Fatal("expected scrollbar visible when natural height 640 exceeds 600px viewport")
	}
	if workergui.ShouldShowCanvasScrollbar(640, 0) {
		t.Fatal("expected scrollbar hidden when viewport is unmapped (height 0)")
	}

	if workergui.ShouldShowScrollbarFractions([]string{"0.0", "1.0"}) {
		t.Fatal("expected text scrollbar hidden for full [0.0, 1.0] fraction")
	}
	if workergui.ShouldShowScrollbarFractions([]string{"0", "0.9999"}) {
		t.Fatal("expected subpixel [0, 0.9999] fraction to keep scrollbar hidden")
	}
	if !workergui.ShouldShowScrollbarFractions([]string{"0.0", "0.75"}) {
		t.Fatal("expected scrollbar visible when bottom fraction is 0.75")
	}
	if !workergui.ShouldShowScrollbarFractions([]string{"0.15", "1.0"}) {
		t.Fatal("expected scrollbar visible when top fraction is scrolled to 0.15")
	}
	if workergui.ShouldShowScrollbarFractions([]string{"invalid"}) {
		t.Fatal("expected malformed fraction slice to return false")
	}
}

func TestViewportResizeShortCircuitChecklistSignatureAndErrorLocalization(t *testing.T) {
	if workergui.ShouldApplyViewportResize(false, 40, 500, 0, 0) {
		t.Fatal("expected unmapped narrow canvas (<60px) to skip resize")
	}
	if workergui.ShouldApplyViewportResize(false, 640, 480, 640, 480) {
		t.Fatal("expected identical dimensions with clean content to short-circuit")
	}
	if !workergui.ShouldApplyViewportResize(true, 640, 480, 640, 480) {
		t.Fatal("expected dirty content flag to force viewport measurement")
	}
	if !workergui.ShouldApplyViewportResize(false, 720, 480, 640, 480) {
		t.Fatal("expected width change to trigger viewport resize")
	}

	baseSnap := worker.StatusSnapshot{
		CachedModelSHA256s: []string{"aaaa1111", "bbbb2222"},
		CatalogModels: []protocol.CatalogModelView{{
			ID:            "qwen2.5-7b",
			Name:          "Qwen 2.5 7B",
			ModelSHA256:   "cccc3333",
			FileSizeMiB:   4200,
			TargetVramMiB: 5120,
			SizeLabel:     "7B",
			Quantization:  "Q4_K_M",
			Format:        "gguf",
			Backend:       "llama_local",
			Task:          "text_generation",
		}},
	}
	sig1 := workergui.BuildCatalogModelChecklistSig("en", "multi", "normal", workergui.BandStandard, 8192, []string{"m2", "m1"}, baseSnap)

	reorderedSnap := baseSnap
	reorderedSnap.CachedModelSHA256s = []string{"BBBB2222", "aaaa1111"}
	sigReordered := workergui.BuildCatalogModelChecklistSig("en", "multi", "normal", workergui.BandStandard, 8192, []string{"m1", "m2"}, reorderedSnap)
	if sig1 != sigReordered {
		t.Fatalf("expected deterministic signature regardless of cached SHA / selected slice order, got %s vs %s", sig1, sigReordered)
	}

	cachedFlipSnap := baseSnap
	cachedFlipSnap.CachedModelSHA256s = []string{"aaaa1111", "bbbb2222", "cccc3333"}
	if sigCached := workergui.BuildCatalogModelChecklistSig("en", "multi", "normal", workergui.BandStandard, 8192, []string{"m1", "m2"}, cachedFlipSnap); sigCached == sig1 {
		t.Fatal("expected newly cached model SHA to invalidate checklist signature immediately")
	}

	metaMutSnap := baseSnap
	metaMutSnap.CatalogModels = []protocol.CatalogModelView{baseSnap.CatalogModels[0]}
	metaMutSnap.CatalogModels[0].Quantization = "Q8_0"
	if sigQuant := workergui.BuildCatalogModelChecklistSig("en", "multi", "normal", workergui.BandStandard, 8192, []string{"m1", "m2"}, metaMutSnap); sigQuant == sig1 {
		t.Fatal("expected quantization change to invalidate checklist signature")
	}

	capEN := workergui.FormatModelRowCaption("en", baseSnap.CatalogModels[0], "SOLO OK", true)
	capPT := workergui.FormatModelRowCaption("pt-BR", baseSnap.CatalogModels[0], "SOLO OK", true)
	if !strings.Contains(capEN, "Cached") || !strings.Contains(capPT, "Em Cache") {
		t.Fatalf("expected localized cached badge in captions, got en=%q pt=%q", capEN, capPT)
	}

	if gotPT := workergui.FormatWorkerError("pt-BR", "Connection error — retrying automatically"); gotPT != "Erro de conexão — reconectando automaticamente" {
		t.Fatalf("expected pt-BR retry fallback translation, got %q", gotPT)
	}
}
