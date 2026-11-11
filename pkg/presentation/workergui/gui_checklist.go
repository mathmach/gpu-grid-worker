package workergui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
	. "modernc.org/tk9.0"
)

type catalogChecklistSpec struct {
	frame        *TFrameWidget
	cbState      string
	lastSig      *string
	viewport     *canvasViewportState
	syncViewport func()
}

func BuildCatalogModelChecklistSig(
	lang string,
	mode string,
	cbState string,
	band LayoutBand,
	checkedVRAM int,
	selectedModels []string,
	s worker.StatusSnapshot,
) string {
	h := sha256.New()
	fmt.Fprintf(
		h,
		"lang=%s|mode=%s|state=%s|band=%s|vram=%d|disk=%d:%v|sel=%s|cached=%s|n=%d",
		lang,
		mode,
		cbState,
		band,
		checkedVRAM,
		s.DiskFreeMiB,
		s.DiskFreeProbed,
		normalizeSortedSig(selectedModels),
		normalizeLowerSortedSig(s.CachedModelSHA256s),
		len(s.CatalogModels),
	)
	for _, m := range s.CatalogModels {
		fmt.Fprintf(
			h,
			"|id=%s|name=%s|sha=%s|dl=%d|vram=%d|params=%s|quant=%s|fmt=%s|eng=%s|task=%s|blk=%d|res=%v|mesh=%v|rate=%.6f",
			m.ID,
			m.Name,
			strings.ToLower(strings.TrimSpace(m.ModelSHA256)),
			m.FileSizeMiB,
			m.TargetVramMiB,
			m.SizeLabel,
			m.Quantization,
			m.Format,
			m.Backend,
			m.Task,
			m.BlockCount,
			m.RemoteOnly,
			m.AllowsMesh,
			m.BestClearingPer1M,
		)
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:])
}

func (g *guiController) syncCatalogModelChecklist(
	s worker.StatusSnapshot,
	checkedVRAM int,
	l GUILabels,
	spec catalogChecklistSpec,
) {
	if spec.frame == nil || spec.lastSig == nil || spec.viewport == nil {
		return
	}
	sig := BuildCatalogModelChecklistSig(
		g.lang,
		g.modelMode,
		spec.cbState,
		g.currentBand,
		checkedVRAM,
		g.selectedModelSlice(),
		s,
	)
	if sig == *spec.lastSig {
		return
	}
	*spec.lastSig = sig
	spec.viewport.contentDirty = true
	for _, child := range WinfoChildren(spec.frame.Window) {
		Destroy(child)
	}
	if g.modelMode == protocol.ModelModeAuto {
		spec.syncViewport()
		return
	}
	if len(s.CatalogModels) == 0 {
		configureChecklistColumns(spec.frame, 1)
		lbl := spec.frame.TLabel(Style("Field.TLabel"), Txt(l.NoModels))
		Grid(lbl, Row(0), Column(0), Sticky("w"))
		spec.syncViewport()
		return
	}
	cols := effectiveChecklistCols(g.currentBand, len(s.CatalogModels))
	configureChecklistColumns(spec.frame, cols)
	for idx, m := range s.CatalogModels {
		g.renderChecklistModelRow(spec.frame, idx, cols, spec.cbState, m, s, checkedVRAM, l)
	}
	spec.syncViewport()
}

func (g *guiController) renderChecklistModelRow(
	frame *TFrameWidget,
	idx int,
	cols int,
	cbState string,
	m protocol.CatalogModelView,
	s worker.StatusSnapshot,
	checkedVRAM int,
	l GUILabels,
) {
	fit := ResolveModelRowFitLabel(m, checkedVRAM, s.CachedModelSHA256s, s.DiskFreeMiB, s.DiskFreeProbed, l)
	cached := nodecore.WorkerHasCachedSHA256(s.CachedModelSHA256s, m.ModelSHA256)
	caption := FormatModelRowCaption(g.lang, m, fit, cached)
	initVar := boolCheckVar(g.selectedModels[m.ID])
	modelID := m.ID
	var cb *TCheckbuttonWidget
	cb = frame.TCheckbutton(
		Style("Gpu.TCheckbutton"),
		Txt(caption),
		Variable(initVar),
		State(cbState),
		Command(func() { g.onModelCheckToggled(modelID, cb) }),
	)
	gridChecklistChild(cb.Window, idx, cols)
}

func ResolveModelRowFitLabel(
	m protocol.CatalogModelView,
	checkedVRAM int,
	cachedDigests []string,
	diskFreeMiB int,
	diskFreeProbed bool,
	l GUILabels,
) string {
	if diskFreeProbed && m.FileSizeMiB > 0 && !nodecore.WorkerHasCachedSHA256(cachedDigests, m.ModelSHA256) && diskFreeMiB < m.FileSizeMiB {
		return l.FitInsufficientDisk
	}
	if checkedVRAM > 0 && m.TargetVramMiB > 0 && checkedVRAM < m.TargetVramMiB {
		task, err := nodecore.ParseModelTask(m.Task)
		format := nodecore.ModelFormat(strings.ToLower(strings.TrimSpace(m.Format)))
		if err == nil && m.BlockCount > 0 && nodecore.AllowsMeshTopology(task, format) {
			return l.FitMesh
		}
		return l.FitInsufficientVRAM
	}
	return l.FitSolo
}

func FormatModelRowCaption(lang string, m protocol.CatalogModelView, fit string, cached bool) string {
	suffix := fit
	if cached {
		suffix = fit + " · " + localizedPair(lang, "Em Cache", "Cached")
	}
	if m.FileSizeMiB > 0 {
		dlLbl := localizedPair(lang, "Tam.", "DL")
		return fmt.Sprintf("%s  [%s %d MiB · VRAM %d MiB · %s]", m.ID, dlLbl, m.FileSizeMiB, m.TargetVramMiB, suffix)
	}
	return fmt.Sprintf("%s  [%d MiB · %s]", m.ID, m.TargetVramMiB, suffix)
}
