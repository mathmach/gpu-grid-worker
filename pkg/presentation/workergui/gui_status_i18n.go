package workergui

import (
	"fmt"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type UIMode string

const (
	UIModeSimple   UIMode = UIMode(nodecore.WorkerUISimple)
	UIModeAdvanced UIMode = UIMode(nodecore.WorkerUIAdvanced)
)

func ParseUIMode(raw string) (UIMode, error) {
	if strings.TrimSpace(raw) == "" {
		return UIModeSimple, nil
	}
	parsed, err := nodecore.ParseWorkerUIMode(raw)
	if err != nil {
		return "", err
	}
	return UIMode(parsed), nil
}

type advTab string

const (
	advTabConfig advTab = "config"
	advTabDebug  advTab = "debug"
)

var statusMapPT = map[string]string{
	"STOPPED":               "PARADO",
	"STARTING":              "INICIANDO",
	"RECONNECTING":          "RECONECTANDO",
	"IDLE":                  "OCIOSO",
	"PAUSED":                "PAUSADO",
	"PAUSE_DRAINING":        "DRENANDO P/ PAUSA",
	"DRAINING":              "DRENANDO",
	"DOWNLOADING":           "BAIXANDO",
	"PRE_DOWNLOADING":       "PRÉ-BAIXANDO MODELOS",
	"PULLING_MODEL":         "BAIXANDO MODELO",
	"STARTING_SOLO":         "INICIANDO SOLO",
	"SOLO_READY":            "SOLO PRONTO",
	"READY":                 "PRONTO",
	"STANDBY":               "EM ESPERA",
	"CONNECTED":             "CONECTADO",
	"WAITING_PEERS":         "FILA MESH",
	"WAITING_PLATFORM_RATE": "AGUARDANDO TARIFA",
	"REBALANCING":           "AQUECENDO",
	"INFERRING":             "PROCESSANDO",
	"PROCESSING":            "PROCESSANDO",
	"QUARANTINED":           "EM QUARENTENA",
	"HARDWARE_PROBE_ERROR":  "FALHA NA GPU",
	"ERROR":                 "ERRO",
}

var statusMapEN = map[string]string{
	"STOPPED":               "STOPPED",
	"STARTING":              "STARTING",
	"RECONNECTING":          "RECONNECTING",
	"IDLE":                  "IDLE",
	"PAUSED":                "PAUSED",
	"PAUSE_DRAINING":        "DRAINING TO PAUSE",
	"DRAINING":              "DRAINING",
	"DOWNLOADING":           "DOWNLOADING",
	"PRE_DOWNLOADING":       "PRE-DOWNLOADING MODELS",
	"PULLING_MODEL":         "PULLING MODEL",
	"STARTING_SOLO":         "STARTING SOLO",
	"SOLO_READY":            "SOLO READY",
	"READY":                 "READY",
	"STANDBY":               "STANDBY",
	"CONNECTED":             "CONNECTED",
	"WAITING_PEERS":         "MESH QUEUE",
	"WAITING_PLATFORM_RATE": "AWAITING RATE",
	"REBALANCING":           "WARMING UP",
	"INFERRING":             "PROCESSING",
	"PROCESSING":            "PROCESSING",
	"QUARANTINED":           "QUARANTINED",
	"HARDWARE_PROBE_ERROR":  "GPU PROBE ERROR",
	"ERROR":                 "ERROR",
}

var pauseReasonMapPT = map[string]string{
	"idle_backend_unavailable":    "Sensor de ociosidade indisponível no sistema",
	"user_active":                 "Auto-Pause: atividade local detectada no PC",
	"manual_pause":                "Pausado manualmente pelo provedor",
	"Paused manually by provider": "Pausado manualmente pelo provedor",
	"Smart Auto-Pause: local desktop activity detected": "Auto-Pause: atividade local detectada no PC",
}

var pauseReasonMapEN = map[string]string{
	"idle_backend_unavailable":    "OS idle sensor unavailable",
	"user_active":                 "Smart Auto-Pause: local desktop activity detected",
	"manual_pause":                "Paused manually by provider",
	"Paused manually by provider": "Paused manually by provider",
	"Smart Auto-Pause: local desktop activity detected": "Smart Auto-Pause: local desktop activity detected",
}

func FormatStatusLabel(lang, raw string) string {
	key := strings.ToUpper(strings.TrimSpace(raw))
	if lang == "en" {
		if mapped, ok := statusMapEN[key]; ok {
			return mapped
		}
		return raw
	}
	if mapped, ok := statusMapPT[key]; ok {
		return mapped
	}
	return raw
}

func FormatPauseReason(lang, raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if lang == "en" {
		if mapped, ok := pauseReasonMapEN[trimmed]; ok {
			return mapped
		}
		return trimmed
	}
	if mapped, ok := pauseReasonMapPT[trimmed]; ok {
		return mapped
	}
	return trimmed
}

var simpleStatusPT = map[string]string{
	"STARTING":              "Iniciando e conectando...",
	"RECONNECTING":          "Reconectando ao servidor...",
	"DOWNLOADING":           "Baixando motor ou modelo de IA...",
	"PRE_DOWNLOADING":       "Fazendo pré-download dos modelos selecionados...",
	"PULLING_MODEL":         "Baixando modelo de IA...",
	"STARTING_SOLO":         "Preparando motor de IA...",
	"PAUSE_DRAINING":        "Finalizando tarefa para pausar...",
	"DRAINING":              "Finalizando tarefa atual...",
	"INFERRING":             "Processando IA agora (gerando ganhos)",
	"PROCESSING":            "Processando IA agora (gerando ganhos)",
	"SOLO_READY":            "Ativo — pronto para tarefas de IA",
	"READY":                 "Ativo — pronto para tarefas de IA",
	"STANDBY":               "Ativo — aguardando tarefas na rede",
	"CONNECTED":             "Conectado — aguardando alocação",
	"WAITING_PEERS":         "Conectado — aguardando parceiros de rede",
	"WAITING_PLATFORM_RATE": "Aguardando configuração da tarifa base na plataforma",
	"REBALANCING":           "Aquecendo modelo na GPU...",
	"QUARANTINED":           "Em verificação automática de saúde",
	"HARDWARE_PROBE_ERROR":  "Falha ao detectar GPU Vulkan — verifique os drivers",
	"ERROR":                 "Atenção — verifique o aviso acima",
}

var simpleStatusEN = map[string]string{
	"STARTING":              "Starting and connecting...",
	"RECONNECTING":          "Reconnecting to server...",
	"DOWNLOADING":           "Downloading AI runtime or model...",
	"PRE_DOWNLOADING":       "Pre-downloading selected AI models...",
	"PULLING_MODEL":         "Downloading AI model...",
	"STARTING_SOLO":         "Preparing AI engine...",
	"PAUSE_DRAINING":        "Finishing active job to pause...",
	"DRAINING":              "Finishing current job...",
	"INFERRING":             "Processing AI job now (earning)",
	"PROCESSING":            "Processing AI job now (earning)",
	"SOLO_READY":            "Active — ready for AI jobs",
	"READY":                 "Active — ready for AI jobs",
	"STANDBY":               "Active — waiting for network jobs",
	"CONNECTED":             "Connected — awaiting assignment",
	"WAITING_PEERS":         "Connected — waiting for pool peers",
	"WAITING_PLATFORM_RATE": "Awaiting platform compute rate configuration",
	"REBALANCING":           "Warming up model on GPU...",
	"QUARANTINED":           "Running automatic health check",
	"HARDWARE_PROBE_ERROR":  "Vulkan GPU detection failed — check graphics drivers",
	"ERROR":                 "Attention — check warning above",
}

func FormatSimpleStatus(lang string, s worker.StatusSnapshot) string {
	key := strings.ToUpper(strings.TrimSpace(s.Status))
	if !s.Running {
		if key == worker.StatusProbeError {
			return lookupSimpleStatus(lang, key, s.Status)
		}
		return localizedPair(lang, "Pronto para iniciar", "Ready to start")
	}
	if s.PauseDraining || key == worker.StatusPauseDraining {
		return lookupSimpleStatus(lang, worker.StatusPauseDraining, s.Status)
	}
	if !s.ManualPaused && key == worker.StatusPreDownloading && strings.TrimSpace(s.PreDownloadModelID) != "" {
		return formatPreDownloadSimpleStatus(lang, s)
	}
	if s.Paused {
		return formatSimplePausedStatus(lang, s.PauseReason)
	}
	return lookupSimpleStatus(lang, key, s.Status)
}

func formatPreDownloadSimpleStatus(lang string, s worker.StatusSnapshot) string {
	idx := max(1, s.PreDownloadIndex)
	tot := max(idx, s.PreDownloadTotal)
	pct := min(100, max(0, s.PreDownloadPct))
	if lang == "en" {
		return fmt.Sprintf("Pre-downloading (%d/%d · %d%%): %s", idx, tot, pct, s.PreDownloadModelID)
	}
	return fmt.Sprintf("Pré-baixando (%d/%d · %d%%): %s", idx, tot, pct, s.PreDownloadModelID)
}

func lookupSimpleStatus(lang, key, fallback string) string {
	if lang == "en" {
		if mapped, ok := simpleStatusEN[key]; ok {
			return mapped
		}
		return FormatStatusLabel(lang, fallback)
	}
	if mapped, ok := simpleStatusPT[key]; ok {
		return mapped
	}
	return FormatStatusLabel(lang, fallback)
}

func formatSimplePausedStatus(lang, reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "user_active" || strings.Contains(strings.ToLower(trimmed), "activity") {
		return localizedPair(lang, "Em pausa automática (você está usando o PC)", "Auto-paused (PC in active use)")
	}
	return localizedPair(lang, "Pausado (0 MiB de GPU em uso)", "Paused (0 MiB GPU in use)")
}

func FormatSimpleHardwareSummary(lang string, gpus []protocol.GpuInfo, selectedIDs []string) string {
	chosen := pickSummaryGPUs(gpus, selectedIDs)
	if len(chosen) == 0 {
		return localizedPair(lang, "Placa de vídeo: Detecção automática ao iniciar", "Graphics card: Auto-detected on start")
	}
	names := make([]string, 0, len(chosen))
	for _, g := range chosen {
		names = append(names, fmt.Sprintf("%s (%d MiB)", g.Name, g.VramTotalMiB))
	}
	prefix := localizedPair(lang, "Placa de vídeo: ", "Graphics card: ")
	return prefix + strings.Join(names, " + ")
}

func pickSummaryGPUs(gpus []protocol.GpuInfo, selectedIDs []string) []protocol.GpuInfo {
	if len(gpus) == 0 {
		return nil
	}
	if len(selectedIDs) == 0 {
		return gpus
	}
	sel := buildLowerSet(selectedIDs)
	out := make([]protocol.GpuInfo, 0, len(selectedIDs))
	for _, g := range gpus {
		if sel[strings.ToLower(g.DeviceID)] {
			out = append(out, g)
		}
	}
	if len(out) > 0 {
		return out
	}
	return gpus
}

func FormatAccountSummary(lang, email string, hasToken bool) string {
	cleanEmail := strings.TrimSpace(email)
	if cleanEmail != "" {
		return localizedPair(lang, "Conta: ", "Account: ") + cleanEmail
	}
	if hasToken {
		return localizedPair(lang, "Conta vinculada via Token", "Account linked via Token")
	}
	return localizedPair(lang, "Nenhuma conta vinculada", "No account linked")
}

func localizedPair(lang, pt, en string) string {
	if lang == "en" {
		return en
	}
	return pt
}
