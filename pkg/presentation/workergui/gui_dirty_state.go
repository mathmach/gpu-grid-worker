package workergui

import (
	"sort"
	"strconv"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
	. "modernc.org/tk9.0"
)

type syncedFormBaseline struct {
	gwURL       string
	workerToken string
	workerName  string
	idleSecText string
	modelMode   string
	modelsSig   string
	gpusSig     string
	modelsDirty bool
	gpusDirty   bool
}

type cachedUIWidgetState struct {
	bannerSig          string
	runningControlsSig string
	simpleLoginSig     string
	loginCancelVisible bool
	simpleAcct         string
	simpleStat         string
	simpleTok          string
	simpleEarn         string
	simpleHw           string
	simpleModeBtnState string
	simpleAutoTxt      string
	simplePaneVisible  bool
	simpleSwitchState  string
	simpleDiscVisible  bool
	simpleActionSig    string
	advModelVisible    bool
	kpiStatus          string
	kpiModel           string
	kpiClearing        string
	kpiBalance         string
	dbgMeta            string
}

func (g *guiController) resetCachedWidgetState() {
	g.widgetCache.bannerSig = ""
	g.widgetCache.runningControlsSig = ""
	g.widgetCache.simpleLoginSig = ""
	g.widgetCache.simpleAcct = ""
	g.widgetCache.simpleStat = ""
	g.widgetCache.simpleTok = ""
	g.widgetCache.simpleEarn = ""
	g.widgetCache.simpleHw = ""
	g.widgetCache.simpleModeBtnState = ""
	g.widgetCache.simpleAutoTxt = ""
	g.widgetCache.simpleSwitchState = ""
	g.widgetCache.simpleActionSig = ""
	g.widgetCache.kpiStatus = ""
	g.widgetCache.kpiModel = ""
	g.widgetCache.kpiClearing = ""
	g.widgetCache.kpiBalance = ""
	g.widgetCache.dbgMeta = ""
}

func (g *guiController) initBaseline(s worker.StatusSnapshot) {
	mode := protocol.ModelModeAuto
	if s.ModelMode == protocol.ModelModeMulti {
		mode = protocol.ModelModeMulti
	}
	g.synced = syncedFormBaseline{
		gwURL:       s.GatewayURL,
		workerToken: s.WorkerToken,
		workerName:  s.WorkerName,
		idleSecText: strconv.Itoa(s.IdleThresholdSec),
		modelMode:   mode,
		modelsSig:   normalizeSortedSig(s.SelectedModels),
		gpusSig:     normalizeLowerSortedSig(s.SelectedIDs),
	}
}

func syncEntryIfClean(entry *TEntryWidget, prevSynced *string, nextVal string) {
	if entry == nil {
		return
	}
	cur := entry.Textvariable()
	if cur == *prevSynced {
		if nextVal != cur {
			entry.Configure(Textvariable(nextVal))
		}
		*prevSynced = nextVal
		return
	}
	if strings.TrimSpace(cur) == "" && strings.TrimSpace(nextVal) != "" && strings.TrimSpace(*prevSynced) == "" {
		entry.Configure(Textvariable(nextVal))
		*prevSynced = nextVal
	}
}

func syncCheckIfClean(cb *TCheckbuttonWidget, dirty bool, nextVal string) {
	if cb == nil || dirty {
		return
	}
	if cb.Variable() != nextVal {
		cb.Configure(Variable(nextVal))
	}
}

func (g *guiController) commitScheduleBaseline(mode string, sec int) {
	idleVar := boolCheckVar(mode == "idle")
	g.idleCb.Configure(Variable(idleVar))
	if sec > 0 {
		secTxt := strconv.Itoa(sec)
		g.idleSecEntry.Configure(Textvariable(secTxt))
		g.synced.idleSecText = secTxt
	}
}

func (g *guiController) resetTokenEntryBaseline(gw, name string) {
	g.gwEntry.Configure(Textvariable(gw))
	g.tokEntry.Configure(Textvariable(""))
	g.nameEntry.Configure(Textvariable(name))
	g.synced.gwURL = gw
	g.synced.workerToken = ""
	g.synced.workerName = name
}

func (g *guiController) commitModelBaseline(mode string, sel []string) {
	normMode := protocol.ModelModeAuto
	if mode == protocol.ModelModeMulti {
		normMode = protocol.ModelModeMulti
	}
	g.modelMode = normMode
	g.selectedModels = make(map[string]bool, len(sel))
	for _, m := range sel {
		g.selectedModels[m] = true
	}
	g.synced.modelMode = normMode
	g.synced.modelsSig = normalizeSortedSig(sel)
	g.synced.modelsDirty = false
	g.refreshModeButtonStyles()
	g.lastModelSig = ""
	g.lastSimpleModelSig = ""
}

func (g *guiController) commitGPUBaseline(ids []string) {
	g.synced.gpusSig = normalizeLowerSortedSig(ids)
	g.synced.gpusDirty = false
	g.lastGpuSig = ""
}

func (g *guiController) syncWidgetsFromSnapshot(s worker.StatusSnapshot) {
	g.gwEntry.Configure(Textvariable(s.GatewayURL))
	g.tokEntry.Configure(Textvariable(s.WorkerToken))
	g.nameEntry.Configure(Textvariable(s.WorkerName))
	g.commitScheduleBaseline(s.ScheduleMode, s.IdleThresholdSec)
	g.commitModelBaseline(s.ModelMode, s.SelectedModels)
	g.commitGPUBaseline(s.SelectedIDs)
	g.initBaseline(s)
	g.lastCfgSig = BuildExternalConfigSig(s)
}

func normalizeSortedSig(items []string) string {
	if len(items) == 0 {
		return ""
	}
	cp := make([]string, 0, len(items))
	for _, item := range items {
		if t := strings.TrimSpace(item); t != "" {
			cp = append(cp, t)
		}
	}
	sort.Strings(cp)
	return strings.Join(cp, ",")
}

func normalizeLowerSortedSig(items []string) string {
	if len(items) == 0 {
		return ""
	}
	cp := make([]string, 0, len(items))
	for _, item := range items {
		if t := strings.ToLower(strings.TrimSpace(item)); t != "" {
			cp = append(cp, t)
		}
	}
	sort.Strings(cp)
	return strings.Join(cp, ",")
}
