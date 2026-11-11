package workergui_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/presentation/workergui"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func TestClassifyLayoutBandAndGeometry(t *testing.T) {
	if got := workergui.ClassifyLayoutBand(380); got != workergui.BandCompact {
		t.Fatalf("expected compact at 380px, got %s", got)
	}
	if got := workergui.ClassifyLayoutBand(639); got != workergui.BandCompact {
		t.Fatalf("expected compact at 639px, got %s", got)
	}
	if got := workergui.ClassifyLayoutBand(640); got != workergui.BandStandard {
		t.Fatalf("expected standard at 640px, got %s", got)
	}
	if got := workergui.ClassifyLayoutBand(1079); got != workergui.BandStandard {
		t.Fatalf("expected standard at 1079px, got %s", got)
	}
	if got := workergui.ClassifyLayoutBand(1080); got != workergui.BandWide {
		t.Fatalf("expected wide at 1080px, got %s", got)
	}
	if got := workergui.ClassifyLayoutBand(3840); got != workergui.BandWide {
		t.Fatalf("expected wide at 3840px, got %s", got)
	}

	if w := workergui.ComputeWrapLength(260); w != 240 {
		t.Fatalf("expected clamped wrap length 240, got %d", w)
	}
	if w := workergui.ComputeWrapLength(900); w != 852 {
		t.Fatalf("expected wrap length 852, got %d", w)
	}

	if g := workergui.ComputeInitialGeometry(1920, 1080); g != "1140x760" {
		t.Fatalf("expected 1140x760 on 1080p, got %s", g)
	}
	if g := workergui.ComputeInitialGeometry(1366, 768); g != "920x688" {
		t.Fatalf("expected height clamped to 688 on 768p laptop, got %s", g)
	}
}

func TestLabelsCompletenessBilingual(t *testing.T) {
	for _, lang := range []string{"pt-BR", "en"} {
		lbl := workergui.LabelsFor(lang)
		rv := reflect.ValueOf(lbl)
		rt := rv.Type()
		for i := range rv.NumField() {
			if rv.Field(i).String() == "" {
				t.Fatalf("empty label field %s for locale %s", rt.Field(i).Name, lang)
			}
		}
	}

	if got := workergui.FormatStatusLabel("pt-BR", "READY"); got != "PRONTO" {
		t.Fatalf("expected PRONTO, got %s", got)
	}
	if got := workergui.FormatStatusLabel("en", "READY"); got != "READY" {
		t.Fatalf("expected READY, got %s", got)
	}
}

func TestFormatWorkerStatusesAndPauseReasonsBilingual(t *testing.T) {
	statuses := []string{
		"STOPPED", "STARTING", "RECONNECTING", "PAUSED", "PAUSE_DRAINING",
		"DRAINING", "PULLING_MODEL", "STARTING_SOLO", "SOLO_READY", "READY",
		"STANDBY", "CONNECTED", "WAITING_PEERS", "WAITING_PLATFORM_RATE",
		"REBALANCING", "INFERRING", "PROCESSING", "QUARANTINED", "ERROR",
	}
	for _, st := range statuses {
		pt := workergui.FormatStatusLabel("pt-BR", st)
		en := workergui.FormatStatusLabel("en", st)
		if pt == "" || en == "" {
			t.Fatalf("expected non-empty labels for %s, got pt=%q en=%q", st, pt, en)
		}
		if strings.Contains(pt, "_") || strings.Contains(en, "_") {
			t.Fatalf("expected localized human-readable label without underscore for %s, got pt=%q en=%q", st, pt, en)
		}
	}

	reasons := []string{"idle_backend_unavailable", "user_active", "manual_pause"}
	for _, r := range reasons {
		pt := workergui.FormatPauseReason("pt-BR", r)
		en := workergui.FormatPauseReason("en", r)
		if pt == "" || en == "" || pt == r || en == r || pt == en {
			t.Fatalf("expected distinct localized pause reason for %s, got pt=%q en=%q", r, pt, en)
		}
	}
}

func TestEventCoalescerBatchesRapidNotifications(t *testing.T) {
	var queued []func()
	var syncRuns int
	coalesce := workergui.NewEventCoalescer(func(fn func()) {
		queued = append(queued, fn)
	}, func() {
		syncRuns++
	})

	for range 10 {
		coalesce()
	}
	if len(queued) != 1 {
		t.Fatalf("expected 10 burst events to coalesce into 1 queued UI post, got %d", len(queued))
	}
	queued[0]()
	if syncRuns != 1 {
		t.Fatalf("expected 1 syncUI execution after flush, got %d", syncRuns)
	}

	coalesce()
	if len(queued) != 2 {
		t.Fatalf("expected new notification after flush to schedule next UI post, got %d", len(queued))
	}
	queued[1]()
	if syncRuns != 2 {
		t.Fatalf("expected 2 syncUI executions total, got %d", syncRuns)
	}
}

func TestLocaleSelectOptionsMapping(t *testing.T) {
	opts := workergui.LocaleOptionLabels()
	if len(opts) != 2 || opts[0] != "Português" || opts[1] != "English" {
		t.Fatalf("unexpected locale select options: %+v", opts)
	}
	if idx := workergui.LocaleIndexForCode("pt-BR"); idx != 0 {
		t.Fatalf("expected index 0 for pt-BR, got %d", idx)
	}
	if idx := workergui.LocaleIndexForCode("en"); idx != 1 {
		t.Fatalf("expected index 1 for en, got %d", idx)
	}
	if code := workergui.LocaleCodeForIndex(0); code != "pt-BR" {
		t.Fatalf("expected pt-BR at index 0, got %s", code)
	}
	if code := workergui.LocaleCodeForIndex(1); code != "en" {
		t.Fatalf("expected en at index 1, got %s", code)
	}
	if fallback := workergui.LocaleCodeForIndex(99); fallback != "en" {
		t.Fatalf("expected fallback en for out-of-range index, got %s", fallback)
	}
}

func TestParseIdleInputs(t *testing.T) {
	if sec, ok := workergui.ParseIdleSecInput("120"); !ok || sec != 120 {
		t.Fatalf("expected 120s valid, got %d %v", sec, ok)
	}
	for _, invalid := range []string{"", "14", "3601", "abc", "-5"} {
		if _, ok := workergui.ParseIdleSecInput(invalid); ok {
			t.Fatalf("expected ParseIdleSecInput(%q) to fail", invalid)
		}
	}
}

func TestResolveBannerSeparatesErrorsFromStandby(t *testing.T) {
	errMsg, isErr := workergui.ResolveBannerContent("pt-BR", "validation failed", worker.StatusSnapshot{})
	if !isErr || errMsg != "validation failed" {
		t.Fatalf("expected error banner, got msg=%q isErr=%v", errMsg, isErr)
	}

	pauseMsg, isPauseErr := workergui.ResolveBannerContent("pt-BR", "", worker.StatusSnapshot{
		Running:     true,
		Paused:      true,
		PauseReason: "Paused manually by provider",
	})
	if isPauseErr || !strings.Contains(pauseMsg, "0 MiB") || !strings.Contains(pauseMsg, "Pausado manualmente") {
		t.Fatalf("expected non-error localized 0 MiB pause banner, got msg=%q isErr=%v", pauseMsg, isPauseErr)
	}

	waitMsg, isWaitErr := workergui.ResolveBannerContent("en", "", worker.StatusSnapshot{
		Running:       true,
		WaitingReason: "Waiting for mesh peers",
	})
	if isWaitErr || waitMsg != "Waiting for mesh peers" {
		t.Fatalf("expected non-error waiting banner, got msg=%q isErr=%v", waitMsg, isWaitErr)
	}

	rateWaitPT, isRateErrPT := workergui.ResolveBannerContent("pt-BR", "", worker.StatusSnapshot{
		Running:       true,
		WaitingReason: "Awaiting platform compute rate configuration",
	})
	rateWaitEN, isRateErrEN := workergui.ResolveBannerContent("en", "", worker.StatusSnapshot{
		Running:       true,
		WaitingReason: "Awaiting platform compute rate configuration",
	})
	if isRateErrPT || isRateErrEN || !strings.Contains(rateWaitPT, "tarifa base") || rateWaitEN != "Awaiting platform compute rate configuration" {
		t.Fatalf("expected localized platform rate waiting banner, got pt=%q en=%q", rateWaitPT, rateWaitEN)
	}

	idleMsg, isIdleErr := workergui.ResolveBannerContent("pt-BR", "", worker.StatusSnapshot{
		Running:    true,
		IdleStatus: "idle_backend_unavailable",
	})
	if isIdleErr || !strings.Contains(idleMsg, "Sensor de ociosidade") {
		t.Fatalf("expected localized idle sensor warning banner, got msg=%q isErr=%v", idleMsg, isIdleErr)
	}
}

func packTouchpadDelta(dx, dy int16) int {
	u := (uint32(uint16(dx)) << 16) | uint32(uint16(dy))
	return int(int32(u))
}

func TestTouchpadScrollPackedDeltaDecodingAndDirection(t *testing.T) {
	cases := []struct {
		dx     int16
		dy     int16
		wantPx int
	}{
		{dx: 0, dy: 8, wantPx: -8},
		{dx: 0, dy: -5, wantPx: 5},
		{dx: -3, dy: 12, wantPx: -12},
		{dx: -3, dy: -12, wantPx: 12},
		{dx: 14, dy: -28, wantPx: 28},
		{dx: -9, dy: 0, wantPx: 0},
	}

	rawDownOnly := packTouchpadDelta(0, -5)
	if rawDownOnly <= 0 {
		t.Fatalf("expected Tk packed (0, -5) delta to be positive 32-bit int 65531, got %d", rawDownOnly)
	}

	for _, tc := range cases {
		packed := packTouchpadDelta(tc.dx, tc.dy)
		gotX, gotY := workergui.DecodeTouchpadScrollDeltas(packed)
		if gotX != int(tc.dx) || gotY != int(tc.dy) {
			t.Fatalf("DecodeTouchpadScrollDeltas(%d): got (%d, %d), want (%d, %d)", packed, gotX, gotY, tc.dx, tc.dy)
		}
		if gotPx := workergui.TouchpadScrollPixels(packed); gotPx != tc.wantPx {
			t.Fatalf("TouchpadScrollPixels(%d): got %d, want %d", packed, gotPx, tc.wantPx)
		}
	}
}

func TestMouseWheelAndScrollbarPixelNormalization(t *testing.T) {
	if got := workergui.MouseWheelScrollPixels(120, 0); got != -workergui.CanvasWheelStepPx {
		t.Fatalf("expected +120 wheel notch to scroll -24px, got %d", got)
	}
	if got := workergui.MouseWheelScrollPixels(-120, 0); got != workergui.CanvasWheelStepPx {
		t.Fatalf("expected -120 wheel notch to scroll +24px, got %d", got)
	}
	if got := workergui.MouseWheelScrollPixels(-240, 0); got != 2*workergui.CanvasWheelStepPx {
		t.Fatalf("expected -240 fast wheel to scroll +48px, got %d", got)
	}
	if got := workergui.MouseWheelScrollPixels(30, 0); got != -6 {
		t.Fatalf("expected +30 smooth wheel delta to scroll -6px, got %d", got)
	}
	if got := workergui.MouseWheelScrollPixels(2, 0); got != -1 {
		t.Fatalf("expected tiny positive delta to clamp to -1px, got %d", got)
	}
	if got := workergui.MouseWheelScrollPixels(-2, 0); got != 1 {
		t.Fatalf("expected tiny negative delta to clamp to +1px, got %d", got)
	}
	if got := workergui.MouseWheelScrollPixels(0, -1); got != -workergui.CanvasWheelStepPx {
		t.Fatalf("expected fixedStep -1 to scroll -24px, got %d", got)
	}
	if got := workergui.MouseWheelScrollPixels(0, 1); got != workergui.CanvasWheelStepPx {
		t.Fatalf("expected fixedStep +1 to scroll +24px, got %d", got)
	}
	if got := workergui.MouseWheelScrollPixels(0, 0); got != 0 {
		t.Fatalf("expected zero wheel input to return 0, got %d", got)
	}

	unitUp := workergui.NormalizeScrollbarArgs([]string{"scroll", "-1", "units"})
	if !reflect.DeepEqual(unitUp, []string{"scroll", "-24", "units"}) {
		t.Fatalf("unexpected unitUp scrollbar args: %+v", unitUp)
	}
	floatUnits := workergui.NormalizeScrollbarArgs([]string{"scroll", "3.0", "units"})
	if !reflect.DeepEqual(floatUnits, []string{"scroll", "72", "units"}) {
		t.Fatalf("unexpected floatUnits scrollbar args: %+v", floatUnits)
	}
	moveto := workergui.NormalizeScrollbarArgs([]string{"moveto", "0.42"})
	if !reflect.DeepEqual(moveto, []string{"moveto", "0.42"}) {
		t.Fatalf("expected moveto args unchanged, got %+v", moveto)
	}
	pages := workergui.NormalizeScrollbarArgs([]string{"scroll", "1", "pages"})
	if !reflect.DeepEqual(pages, []string{"scroll", "1", "pages"}) {
		t.Fatalf("expected pages args unchanged, got %+v", pages)
	}
}

func TestCanvasScrollRegionFormattingAndWidgetHierarchyGuard(t *testing.T) {
	exact := workergui.FormatCanvasScrollRegion(520, 650)
	if exact != "0 0 520 650" {
		t.Fatalf("expected deterministic scrollregion 0 0 520 650, got %q", exact)
	}
	clampedZero := workergui.FormatCanvasScrollRegion(-10, -5)
	if clampedZero != "0 0 0 0" {
		t.Fatalf("expected negative dimensions clamped to 0 0 0 0, got %q", clampedZero)
	}

	if !workergui.IsWidgetOrDescendant(".w.r.log", ".w.r.log") {
		t.Fatal("expected exact widget match")
	}
	if !workergui.IsWidgetOrDescendant(".w.r.log.txt", ".w.r.log") {
		t.Fatal("expected child widget match")
	}
	if workergui.IsWidgetOrDescendant(".w.r.log2", ".w.r.log") {
		t.Fatal("expected sibling prefix .w.r.log2 not to match .w.r.log")
	}
	if workergui.IsWidgetOrDescendant(".w.r.log", "") {
		t.Fatal("expected empty target path to return false")
	}
}

func TestSimpleModeGeometryAndUIModeNormalization(t *testing.T) {
	if g := workergui.ComputeSimpleGeometry(1920, 1080); g != "680x520" {
		t.Fatalf("expected 680x520 simple geometry on 1080p, got %s", g)
	}
	if g := workergui.ComputeSimpleGeometry(640, 480); g != "580x460" {
		t.Fatalf("expected clamped 580x460 simple geometry on 640x480, got %s", g)
	}
	if m, err := workergui.ParseUIMode(""); err != nil || m != workergui.UIModeSimple {
		t.Fatalf("expected empty uiMode to resolve to simple, got %s err=%v", m, err)
	}
	if m, err := workergui.ParseUIMode("  ADVANCED "); err != nil || m != workergui.UIModeAdvanced {
		t.Fatalf("expected ADVANCED to parse as advanced, got %s err=%v", m, err)
	}
	if _, err := workergui.ParseUIMode("invalid"); err == nil {
		t.Fatal("expected invalid uiMode to return validation error instead of silently falling back to simple")
	}
}

func TestFormatSimpleStatusHardwareAccountAndDebugSummaryBilingual(t *testing.T) {
	for _, lang := range []string{"pt-BR", "en"} {
		stopped := workergui.FormatSimpleStatus(lang, worker.StatusSnapshot{Running: false})
		ready := workergui.FormatSimpleStatus(lang, worker.StatusSnapshot{Running: true, Status: "READY"})
		infer := workergui.FormatSimpleStatus(lang, worker.StatusSnapshot{Running: true, Status: "INFERRING"})
		pull := workergui.FormatSimpleStatus(lang, worker.StatusSnapshot{Running: true, Status: "PULLING_MODEL"})
		autoPause := workergui.FormatSimpleStatus(lang, worker.StatusSnapshot{Running: true, Paused: true, PauseReason: "user_active"})
		manPause := workergui.FormatSimpleStatus(lang, worker.StatusSnapshot{Running: true, Paused: true, PauseReason: "manual_pause"})
		if stopped == "" || ready == "" || infer == "" || pull == "" || autoPause == "" || manPause == "" || autoPause == manPause {
			t.Fatalf("unexpected empty or identical simple statuses for %s: stopped=%q ready=%q autoPause=%q manPause=%q",
				lang, stopped, ready, autoPause, manPause)
		}

		noHw := workergui.FormatSimpleHardwareSummary(lang, nil, nil)
		gpus := []protocol.GpuInfo{
			{DeviceID: "Vulkan0", Name: "Intel UHD", VramTotalMiB: 2048},
			{DeviceID: "Vulkan1", Name: "RTX 4090", VramTotalMiB: 24576},
		}
		selHw := workergui.FormatSimpleHardwareSummary(lang, gpus, []string{"Vulkan1"})
		if noHw == "" || !strings.Contains(selHw, "RTX 4090") || strings.Contains(selHw, "Intel UHD") {
			t.Fatalf("unexpected hardware summary for %s: noHw=%q selHw=%q", lang, noHw, selHw)
		}

		withEmail := workergui.FormatAccountSummary(lang, "gamer@example.com", true)
		tokOnly := workergui.FormatAccountSummary(lang, "", true)
		unlinked := workergui.FormatAccountSummary(lang, "", false)
		if !strings.Contains(withEmail, "gamer@example.com") || tokOnly == "" || unlinked == "" || tokOnly == unlinked {
			t.Fatalf("unexpected account summaries for %s: email=%q tokOnly=%q unlinked=%q", lang, withEmail, tokOnly, unlinked)
		}
	}

	dbg := workergui.FormatDebugTelemetrySummary("en", worker.StatusSnapshot{
		SessionState:    "IN_POOL",
		ExecutionMode:   "SOLO",
		RttMs:           14,
		BlocksProcessed: 7,
		TokensProcessed: 2048,
	})
	if !strings.Contains(dbg, "IN_POOL") || !strings.Contains(dbg, "SOLO") || !strings.Contains(dbg, "14ms") || !strings.Contains(dbg, "2048") {
		t.Fatalf("unexpected debug telemetry summary: %q", dbg)
	}
	dbgPT := workergui.FormatDebugTelemetrySummary("pt-BR", worker.StatusSnapshot{
		SessionState:    "IN_POOL",
		ExecutionMode:   "SOLO",
		RttMs:           14,
		BlocksProcessed: 7,
		TokensProcessed: 2048,
	})
	if dbgPT == dbg || !strings.Contains(dbgPT, "Modo:") {
		t.Fatalf("expected localized pt-BR debug summary, got %q", dbgPT)
	}

	baseSnap := worker.StatusSnapshot{GatewayURL: "http://127.0.0.1:8000", WorkerToken: "wk_tok_1", Currency: "USD"}
	rotSnap := worker.StatusSnapshot{GatewayURL: "http://127.0.0.1:8000", WorkerToken: "wk_tok_2", Currency: "USD"}
	brlSnap := worker.StatusSnapshot{GatewayURL: "http://127.0.0.1:8000", WorkerToken: "wk_tok_2", Currency: "BRL"}
	if workergui.BuildExternalConfigSig(baseSnap) == workergui.BuildExternalConfigSig(rotSnap) {
		t.Fatal("expected BuildExternalConfigSig to detect WorkerToken rotation via local HTTP IPC")
	}
	if workergui.BuildExternalConfigSig(rotSnap) == workergui.BuildExternalConfigSig(brlSnap) {
		t.Fatal("expected BuildExternalConfigSig to detect Currency update")
	}
}

func TestRemoteOnlyModelFitInsufficientVRAMLabelsBilingual(t *testing.T) {
	pt := workergui.LabelsFor("pt-BR")
	en := workergui.LabelsFor("en")
	if pt.FitInsufficientVRAM == "" || en.FitInsufficientVRAM == "" || pt.FitInsufficientVRAM == pt.FitMesh || en.FitInsufficientVRAM == en.FitMesh {
		t.Fatalf("expected distinct non-empty FitInsufficientVRAM labels: pt=%q en=%q", pt.FitInsufficientVRAM, en.FitInsufficientVRAM)
	}
}

func TestResolveModelRowFitLabelPeerLedMeshAndInsufficientDiskBilingual(t *testing.T) {
	sha := strings.Repeat("9", 64)
	remoteGGUF := protocol.CatalogModelView{
		ID:            "qwen2.5-14b-q4_k_m",
		ModelSHA256:   sha,
		Format:        "gguf",
		Task:          "text_generation",
		BlockCount:    48,
		FileSizeMiB:   8192,
		TargetVramMiB: 9216,
		RemoteOnly:    true,
	}
	remoteDiffusion := protocol.CatalogModelView{
		ID:            "flux-dev",
		ModelSHA256:   strings.Repeat("8", 64),
		Format:        "safetensors",
		Task:          "text_to_image",
		BlockCount:    0,
		FileSizeMiB:   12000,
		TargetVramMiB: 16384,
		RemoteOnly:    true,
	}

	for _, lang := range []string{"pt-BR", "en"} {
		lbl := workergui.LabelsFor(lang)
		if got := workergui.ResolveModelRowFitLabel(remoteGGUF, 6144, nil, 4096, true, lbl); got != lbl.FitInsufficientDisk {
			t.Fatalf("[%s] expected FitInsufficientDisk when uncached and free disk < FileSizeMiB, got %q", lang, got)
		}
		if got := workergui.ResolveModelRowFitLabel(remoteGGUF, 6144, []string{sha}, 1024, true, lbl); got != lbl.FitMesh {
			t.Fatalf("[%s] expected FitMesh for cached sub-VRAM RemoteOnly GGUF, got %q", lang, got)
		}
		if got := workergui.ResolveModelRowFitLabel(remoteGGUF, 6144, nil, 16384, true, lbl); got != lbl.FitMesh {
			t.Fatalf("[%s] expected FitMesh for uncached sub-VRAM RemoteOnly GGUF with sufficient disk, got %q", lang, got)
		}
		if got := workergui.ResolveModelRowFitLabel(remoteDiffusion, 8192, nil, 32768, true, lbl); got != lbl.FitInsufficientVRAM {
			t.Fatalf("[%s] expected FitInsufficientVRAM for non-GGUF diffusion model, got %q", lang, got)
		}
		if got := workergui.ResolveModelRowFitLabel(remoteGGUF, 12288, nil, 16384, true, lbl); got != lbl.FitSolo {
			t.Fatalf("[%s] expected FitSolo when checkedVRAM >= TargetVramMiB and disk sufficient, got %q", lang, got)
		}
	}

	meshDbg := workergui.FormatDebugTelemetrySummary("en", worker.StatusSnapshot{
		SessionState:  "IN_POOL",
		ExecutionMode: "MESH",
		MeshRole:      "LEADER",
		RttMs:         8,
	})
	if !strings.Contains(meshDbg, "MESH:LEADER") {
		t.Fatalf("expected MESH:LEADER in debug telemetry summary, got %q", meshDbg)
	}
}

func TestPreDownloadSimpleStatusAndTokenFormattingBilingual(t *testing.T) {
	if pt := workergui.FormatStatusLabel("pt-BR", "PRE_DOWNLOADING"); pt == "" || strings.Contains(pt, "_") {
		t.Fatalf("expected localized pt-BR PRE_DOWNLOADING status, got %q", pt)
	}
	if en := workergui.FormatStatusLabel("en", "PRE_DOWNLOADING"); en == "" || strings.Contains(en, "_") {
		t.Fatalf("expected localized en PRE_DOWNLOADING status, got %q", en)
	}

	snap := worker.StatusSnapshot{
		Running:            true,
		Status:             worker.StatusPreDownloading,
		PreDownloadModelID: "qwen2.5-7b-q4_k_m",
		PreDownloadIndex:   1,
		PreDownloadTotal:   2,
		PreDownloadPct:     64,
	}
	stPT := workergui.FormatSimpleStatus("pt-BR", snap)
	stEN := workergui.FormatSimpleStatus("en", snap)
	if !strings.Contains(stPT, "1/2") || !strings.Contains(stPT, "64%") || !strings.Contains(stPT, "qwen2.5-7b-q4_k_m") {
		t.Fatalf("unexpected pt-BR pre-download status: %q", stPT)
	}
	if !strings.Contains(stEN, "1/2") || !strings.Contains(stEN, "64%") || stPT == stEN {
		t.Fatalf("unexpected en pre-download status: %q", stEN)
	}

	if got := workergui.FormatTokenCount("pt-BR", 0); got != "0" {
		t.Fatalf("expected 0, got %q", got)
	}
	if got := workergui.FormatTokenCount("pt-BR", 128450); got != "128.450" {
		t.Fatalf("expected pt-BR thousands 128.450, got %q", got)
	}
	if got := workergui.FormatTokenCount("en", 128450); got != "128,450" {
		t.Fatalf("expected en thousands 128,450, got %q", got)
	}
	if got := workergui.FormatComputeUnits("pt-BR", 4096); got != "4.096 CU" {
		t.Fatalf("expected pt-BR compute units '4.096 CU', got %q", got)
	}
	if got := workergui.FormatComputeUnits("en", 4096); got != "4,096 CU" {
		t.Fatalf("expected en compute units '4,096 CU', got %q", got)
	}
	if ptTitle := workergui.LabelsFor("pt-BR").SimpleTokensTitle; ptTitle != "PROCESSAMENTO FEITO" {
		t.Fatalf("expected pt-BR SimpleTokensTitle 'PROCESSAMENTO FEITO', got %q", ptTitle)
	}
	if enTitle := workergui.LabelsFor("en").SimpleTokensTitle; enTitle != "COMPUTE PROCESSED" {
		t.Fatalf("expected en SimpleTokensTitle 'COMPUTE PROCESSED', got %q", enTitle)
	}
	dbgCU := workergui.FormatDebugTelemetrySummary("pt-BR", worker.StatusSnapshot{
		SessionState:          "IN_POOL",
		ExecutionMode:         "SOLO",
		RttMs:                 8,
		BlocksProcessed:       1,
		ComputeUnitsProcessed: 2000,
	})
	if !strings.Contains(dbgCU, "2000 CU") {
		t.Fatalf("expected ComputeUnitsProcessed in debug summary, got %q", dbgCU)
	}

	errPT := workergui.FormatWorkerError("pt-BR", "Model pre-download failed: disk full")
	if !strings.Contains(errPT, "pré-download") {
		t.Fatalf("expected localized pre-download error prefix, got %q", errPT)
	}

	sigIdle := workergui.BuildExternalConfigSig(worker.StatusSnapshot{GatewayURL: "http://127.0.0.1:8000"})
	sigPending := workergui.BuildExternalConfigSig(worker.StatusSnapshot{GatewayURL: "http://127.0.0.1:8000", BrowserAuthPending: true})
	sigRunning := workergui.BuildExternalConfigSig(worker.StatusSnapshot{GatewayURL: "http://127.0.0.1:8000", Running: true})
	if sigIdle == sigPending || sigIdle == sigRunning {
		t.Fatal("expected BuildExternalConfigSig to detect BrowserAuthPending and Running transitions")
	}

	autoPausedPreDL := snap
	autoPausedPreDL.Paused = true
	autoPausedPreDL.ManualPaused = false
	autoPausedPreDL.PauseReason = "user_active"
	if got := workergui.FormatSimpleStatus("pt-BR", autoPausedPreDL); !strings.Contains(got, "64%") {
		t.Fatalf("expected auto-paused pre-download to show progress in Simple Mode, got %q", got)
	}
	if banner, _ := workergui.ResolveBannerContent("pt-BR", "", autoPausedPreDL); banner != "" {
		t.Fatalf("expected empty pause banner during active auto-paused pre-download, got %q", banner)
	}

	multiErrPT := workergui.FormatWorkerError("pt-BR", "multi serving role requires at least one selected model")
	if !strings.Contains(multiErrPT, "modelo") {
		t.Fatalf("expected localized multi serving role error, got %q", multiErrPT)
	}
	dbgUnauthPT := workergui.FormatDebugTelemetrySummary("pt-BR", worker.StatusSnapshot{SessionState: "CONNECTED_UNAUTH"})
	if strings.Contains(dbgUnauthPT, "CONNECTED_UNAUTH") {
		t.Fatalf("expected localized CONNECTED_UNAUTH session state, got %q", dbgUnauthPT)
	}
}
