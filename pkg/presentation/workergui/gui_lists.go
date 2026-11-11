package workergui

import (
	"cmp"
	"fmt"
	"sort"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
	. "modernc.org/tk9.0"
)

func (g *guiController) readCheckedGPUs() []string {
	var ids []string
	for _, item := range g.gpuChecks {
		if item.cb.Variable() == "1" {
			ids = append(ids, item.deviceID)
		}
	}
	return ids
}

func (g *guiController) effectiveCheckedGPUs(running bool, activeSel []string) []string {
	if !running && g.synced.gpusDirty && len(g.gpuChecks) > 0 {
		return g.readCheckedGPUs()
	}
	return activeSel
}

func (g *guiController) selectedModelSlice() []string {
	var out []string
	for id, ok := range g.selectedModels {
		if ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (g *guiController) syncGPUList(running bool, gpus []protocol.GpuInfo, activeSel []string) {
	effectiveSel := g.effectiveCheckedGPUs(running, activeSel)
	sig := fmt.Sprintf("lang=%s|band=%s|run=%v|sel=%s|n=%d", g.lang, g.currentBand, running, strings.Join(effectiveSel, ","), len(gpus))
	for _, d := range gpus {
		sig += fmt.Sprintf("|%s:%s:%d:%d", d.DeviceID, d.Name, d.VramFreeMiB, d.VramTotalMiB)
	}
	if sig == g.lastGpuSig {
		return
	}
	g.lastGpuSig = sig
	if len(gpus) > 0 && g.sameGPUDeviceIDs(gpus) {
		g.updateGPUCheckboxesInPlace(running, gpus, effectiveSel)
		return
	}
	for _, child := range WinfoChildren(g.gpuListFrame.Window) {
		Destroy(child)
	}
	g.gpuChecks = nil
	g.advCanvasState.contentDirty = true
	if len(gpus) == 0 {
		configureChecklistColumns(g.gpuListFrame, 1)
		lbl := g.gpuListFrame.TLabel(Style("Field.TLabel"), Txt(LabelsFor(g.lang).NoGPUs))
		Grid(lbl, Row(0), Column(0), Sticky("w"))
		g.syncCanvasViewportWidth()
		return
	}
	g.populateGPUCheckboxes(running, gpus, effectiveSel)
	g.syncCanvasViewportWidth()
}

func (g *guiController) sameGPUDeviceIDs(gpus []protocol.GpuInfo) bool {
	if len(g.gpuChecks) != len(gpus) {
		return false
	}
	for idx, dev := range gpus {
		if g.gpuChecks[idx].deviceID != dev.DeviceID {
			return false
		}
	}
	return true
}

func (g *guiController) updateGPUCheckboxesInPlace(running bool, gpus []protocol.GpuInfo, activeSel []string) {
	selSet := buildLowerSet(activeSel)
	cbState := checkboxState(running)
	for idx, dev := range gpus {
		caption := formatGPUCaption(dev)
		val := boolCheckVar(selSet[strings.ToLower(dev.DeviceID)])
		g.gpuChecks[idx].cb.Configure(Txt(caption), Variable(val), State(cbState))
	}
	relayoutCheckFrame(g.gpuListFrame, g.currentBand)
}

func (g *guiController) populateGPUCheckboxes(running bool, gpus []protocol.GpuInfo, activeSel []string) {
	selSet := buildLowerSet(activeSel)
	cbState := checkboxState(running)
	cols := effectiveChecklistCols(g.currentBand, len(gpus))
	configureChecklistColumns(g.gpuListFrame, cols)
	for idx, dev := range gpus {
		initVar := boolCheckVar(selSet[strings.ToLower(dev.DeviceID)])
		cb := g.gpuListFrame.TCheckbutton(
			Style("Gpu.TCheckbutton"),
			Txt(formatGPUCaption(dev)),
			Variable(initVar),
			State(cbState),
			Command(func() { g.onGPUCheckToggled() }),
		)
		gridChecklistChild(cb.Window, idx, cols)
		g.gpuChecks = append(g.gpuChecks, gpuCheckItem{deviceID: dev.DeviceID, cb: cb})
	}
}

func (g *guiController) onGPUCheckToggled() {
	g.synced.gpusDirty = true
	ids := g.readCheckedGPUs()
	go func() {
		g.eng.UpdateSelectedGPUs(ids)
		g.postUI(func() {
			snap := g.eng.Snapshot()
			g.commitGPUBaseline(snap.SelectedIDs)
			g.lastCfgSig = BuildExternalConfigSig(snap)
			g.syncUI()
		}, false)
	}()
}

func buildLowerSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, id := range items {
		out[strings.ToLower(id)] = true
	}
	return out
}

func checkboxState(running bool) string {
	if running {
		return "disabled"
	}
	return "normal"
}

func formatGPUCaption(dev protocol.GpuInfo) string {
	return fmt.Sprintf("%s: %s  (%d / %d MiB)", dev.DeviceID, dev.Name, dev.VramFreeMiB, dev.VramTotalMiB)
}

func (g *guiController) syncModelList(s worker.StatusSnapshot, checkedVRAM int) {
	g.syncAdvancedModelListVisibility()
	g.syncCatalogModelChecklist(s, checkedVRAM, LabelsFor(g.lang), catalogChecklistSpec{
		frame:        g.modelListFrame,
		cbState:      "normal",
		lastSig:      &g.lastModelSig,
		viewport:     &g.advCanvasState,
		syncViewport: g.syncCanvasViewportWidth,
	})
}

func (g *guiController) onModelCheckToggled(modelID string, cb *TCheckbuttonWidget) {
	g.synced.modelsDirty = true
	if cb.Variable() == "1" {
		g.selectedModels[modelID] = true
	} else {
		delete(g.selectedModels, modelID)
	}
	if g.uiMode == UIModeSimple {
		g.lastModelSig = ""
	} else {
		g.lastSimpleModelSig = ""
	}
}

func configureLabelIfChanged(lbl *TLabelWidget, prev *string, next string) {
	if lbl == nil || *prev == next {
		return
	}
	*prev = next
	lbl.Configure(Txt(next))
}

func (g *guiController) syncKPIsAndLogs(s worker.StatusSnapshot) {
	stTxt := FormatStatusLabel(g.lang, s.Status)
	if s.OrderBookRank > 0 {
		stTxt = fmt.Sprintf("%s (#%d · %.0f%%)", stTxt, s.OrderBookRank, s.ReliabilityPct)
	}
	configureLabelIfChanged(g.kpiStatus, &g.widgetCache.kpiStatus, stTxt)

	modelTxt := cmp.Or(s.ActiveModelID, s.SoloModelID, strings.ToUpper(cmp.Or(s.ModelMode, g.modelMode)))
	configureLabelIfChanged(g.kpiModel, &g.widgetCache.kpiModel, modelTxt)
	configureLabelIfChanged(g.kpiClearing, &g.widgetCache.kpiClearing, formatClearingKPI(s))
	configureLabelIfChanged(g.kpiBalance, &g.widgetCache.kpiBalance, FormatCreditAmount(s.Balance))
	configureLabelIfChanged(g.dbgMetaLbl, &g.widgetCache.dbgMeta, FormatDebugTelemetrySummary(g.lang, s))

	newTitle := LabelsFor(g.lang).WindowTitle
	if s.Running {
		newTitle = fmt.Sprintf("GPU Grid Worker — %s | %s", stTxt, FormatCreditAmount(s.Balance))
	}
	if newTitle != g.lastTitle {
		g.lastTitle = newTitle
		App.WmTitle(newTitle)
	}
	g.syncLogWidget(s.Logs)
}

func formatClearingKPI(s worker.StatusSnapshot) string {
	if s.ClearingPer1M <= 0 {
		return "—"
	}
	if s.TotalLayers > 0 {
		return fmt.Sprintf("%.3f Cr/1M · %d/%d", s.ClearingPer1M, s.AssignedLayers, s.TotalLayers)
	}
	return fmt.Sprintf("%.3f Cr/1M", s.ClearingPer1M)
}

func (g *guiController) syncLogWidget(logs []string) {
	joined := strings.Join(logs, "\n")
	if joined == g.lastLogJoined {
		return
	}
	g.lastLogJoined = joined
	g.logText.Configure(State("normal"))
	g.logText.Delete("1.0", "end")
	if joined != "" {
		g.logText.Insert("end", joined)
		g.logText.See("end")
	}
	g.logText.Configure(State("disabled"))
}
