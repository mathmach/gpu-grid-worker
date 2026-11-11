package workergui

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
)

type errorPrefixTranslation struct {
	prefixEN string
	prefixPT string
}

var workerErrorExactPT = map[string]string{
	"enter your Marketplace Worker Token (wk_...) before starting":              "Informe seu Worker Token (wk_...) antes de iniciar",
	"select at least one GPU before starting":                                   "Selecione pelo menos uma GPU antes de iniciar",
	"none of the selected GPUs were detected on this machine":                   "Nenhuma das GPUs selecionadas foi detectada nesta máquina",
	"no Vulkan GPUs detected on this machine":                                   "Nenhuma GPU compatível com Vulkan foi detectada nesta máquina",
	"HardwareProbeError: hardware inspector is not configured":                  "Falha de detecção de GPU: inspetor de hardware não configurado",
	"HardwareProbeError: unable to determine deterministic machine fingerprint": "Falha de detecção de GPU: não foi possível identificar a máquina",
	"gateway client port is not configured":                                     "Cliente de comunicação com o Gateway não está configurado",
	"gateway login did not return a worker token":                               "O login no Gateway não retornou um Worker Token",
	"stop the worker before changing the linked account":                        "Pare o Worker antes de trocar a conta vinculada",
	"stop the worker before unlinking the account":                              "Pare o Worker antes de desvincular a conta",
	"stop the worker before changing gateway URL, token, name, or GPU devices":  "Pare o Worker antes de alterar URL do Gateway, token, nome ou GPUs",
	"password is required":                                                      "Informe a senha da sua conta",
	"gatewayUrl cannot be empty":                                                "Informe a URL do servidor Gateway",
	"workerToken cannot be empty":                                               "Informe o Worker Token (wk_...)",
	"workerName cannot be empty":                                                "Informe o nome da máquina",
	"multi serving role requires at least one selected model":                   "Selecione pelo menos um modelo de IA na lista",
	"unsupported worker serving role; expected auto or multi":                   "Modo de seleção de modelos inválido (use automático ou manual)",
	"selectedIds cannot contain empty device IDs":                               "A seleção de GPUs contém um identificador vazio",
	"scheduleMode cannot be empty":                                              "Informe o modo de agendamento do Worker",
	"idleThresholdSec must be between 15 and 3600":                              "O tempo de ociosidade deve estar entre 15 e 3600 segundos",
	"modelMode cannot be empty":                                                 "Informe o modo de seleção de modelos",
	"browser launcher is not configured":                                        "Abertura automática do navegador não está configurada",
	"worker portal port is not configured":                                      "Porta de comunicação com o portal do Worker não configurada",
	"missing authorization code or state":                                       "Código de autorização ou estado OAuth ausente",
	"browser login session expired or not started":                              "Sessão de login no navegador expirada ou não iniciada",
	"invalid OAuth state parameter":                                             "Parâmetro de estado OAuth inválido",
	"local ggml-rpc-server exited unexpectedly":                                 "Servidor RPC local da GPU encerrou inesperadamente",
	"solo engine process exited unexpectedly":                                   "Processo do motor SOLO encerrou inesperadamente",
	"solo engine exited before passing /health readiness check":                 "Motor SOLO encerrou antes de responder ao teste de saúde",
	"solo engine supervisor is not configured":                                  "Supervisor do motor SOLO não está configurado",
	"loopback transport is not configured":                                      "Transporte loopback local não está configurado",
	"local ggml-rpc port is not active on MESH leader":                          "Porta RPC local não está ativa no líder MESH",
	"mandatory MESH model verification failed":                                  "Falha na verificação obrigatória do modelo MESH",
	"Failed to prepare Vulkan runtime.":                                         "Falha ao preparar o motor Vulkan.",
	"Failed to allocate local loopback port.":                                   "Falha ao alocar porta loopback local.",
	"Connection error — retrying automatically":                                 "Erro de conexão — reconectando automaticamente",
	"Connection error - retrying automatically":                                 "Erro de conexão — reconectando automaticamente",
}

var workerErrorPrefixesPT = []errorPrefixTranslation{
	{prefixEN: "HardwareProbeError: ", prefixPT: "Falha ao detectar GPU Vulkan: "},
	{prefixEN: "Connection rejected by Gateway: ", prefixPT: "Conexão recusada pelo Gateway: "},
	{prefixEN: "Connection error: ", prefixPT: "Erro de conexão: "},
	{prefixEN: "Invalid Gateway WebSocket URL: ", prefixPT: "URL WebSocket do Gateway inválida: "},
	{prefixEN: "Failed to prepare Vulkan runtime: ", prefixPT: "Falha ao preparar o motor Vulkan: "},
	{prefixEN: "Failed to allocate local TCP port: ", prefixPT: "Falha ao alocar porta TCP local: "},
	{prefixEN: "Failed to allocate local loopback port: ", prefixPT: "Falha ao alocar porta loopback local: "},
	{prefixEN: "Failed to start ggml-rpc-server: ", prefixPT: "Falha ao iniciar servidor RPC da GPU: "},
	{prefixEN: "SOLO engine startup failed: ", prefixPT: "Falha ao iniciar motor SOLO: "},
	{prefixEN: "solo health check cancelled or timed out: ", prefixPT: "Verificação de saúde do motor SOLO expirou ou foi cancelada: "},
	{prefixEN: "MESH model sync failed: ", prefixPT: "Falha ao sincronizar modelo MESH: "},
	{prefixEN: "insufficient free disk space for mandatory model ", prefixPT: "Espaço em disco livre insuficiente para o modelo obrigatório "},
	{prefixEN: "Model pre-download failed: ", prefixPT: "Falha no pré-download do modelo: "},
	{prefixEN: "invalid worker config.json: ", prefixPT: "Arquivo config.json inválido: "},
}

func FormatWorkerError(lang, raw string) string {
	trimmed := strings.TrimSpace(raw)
	if lang == "en" || trimmed == "" {
		return trimmed
	}
	if mapped, ok := workerErrorExactPT[trimmed]; ok {
		return mapped
	}
	for _, rule := range workerErrorPrefixesPT {
		if rest, ok := strings.CutPrefix(trimmed, rule.prefixEN); ok {
			return rule.prefixPT + FormatWorkerError(lang, rest)
		}
	}
	return trimmed
}

func FormatWaitingReason(lang, raw string) string {
	trimmed := strings.TrimSpace(raw)
	if lang == "en" || trimmed == "" {
		return trimmed
	}
	switch trimmed {
	case "Awaiting platform compute rate configuration":
		return "Aguardando configuração da tarifa base de processamento na plataforma"
	case "Waiting for mesh peers":
		return "Aguardando parceiros na fila MESH"
	}
	if rest, ok := strings.CutPrefix(trimmed, "Waiting for MESH peers on "); ok {
		return "Aguardando pares MESH para " + localizeWaitingVRAMClausePT(rest)
	}
	if rest, ok := strings.CutPrefix(trimmed, "Insufficient single-host VRAM for "); ok {
		return "VRAM local insuficiente para " + localizeWaitingVRAMClausePT(rest)
	}
	if rest, ok := strings.CutPrefix(trimmed, "Insufficient VRAM ("); ok {
		return "VRAM insuficiente (" + rest
	}
	if rest, ok := strings.CutPrefix(trimmed, "Waiting for low-RTT MESH peers ("); ok {
		return "Aguardando pares MESH de baixa latência (" + rest
	}
	return trimmed
}

func localizeWaitingVRAMClausePT(clause string) string {
	return strings.ReplaceAll(
		clause,
		" MiB VRAM available, missing ",
		" MiB de VRAM disponíveis, faltam ",
	)
}

func FormatCreditAmount(v float64) string {
	return fmt.Sprintf("%.6f Cr", v)
}

func FormatTokenCount(lang string, tokens int64) string {
	if tokens <= 0 {
		return "0"
	}
	raw := strconv.FormatInt(tokens, 10)
	if len(raw) <= 3 {
		return raw
	}
	sep := "."
	if lang == "en" {
		sep = ","
	}
	rem := len(raw) % 3
	var b strings.Builder
	if rem > 0 {
		b.WriteString(raw[:rem])
	}
	for i := rem; i < len(raw); i += 3 {
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		b.WriteString(raw[i : i+3])
	}
	return b.String()
}

func FormatComputeUnits(lang string, cu int64) string {
	return FormatTokenCount(lang, cu) + " CU"
}

func FormatDebugTelemetrySummary(lang string, s worker.StatusSnapshot) string {
	sess := cmp.Or(strings.TrimSpace(s.SessionState), "CLOSED")
	mode := cmp.Or(strings.TrimSpace(s.ExecutionMode), "-")
	role := strings.TrimSpace(s.MeshRole)
	cu := s.ComputeUnitsProcessed
	if cu <= 0 && s.TokensProcessed > 0 {
		cu = s.TokensProcessed
	}
	if lang == "en" {
		if role != "" {
			mode = fmt.Sprintf("%s:%s", mode, role)
		}
		return fmt.Sprintf(
			"Session: %s  |  Mode: %s  |  RTT: %dms  |  Blocks: %d  |  Compute: %d CU",
			sess, mode, s.RttMs, s.BlocksProcessed, cu,
		)
	}
	sessPT := formatSessionStatePT(sess)
	if role != "" {
		mode = fmt.Sprintf("%s:%s", mode, formatMeshRolePT(role))
	}
	return fmt.Sprintf(
		"Sessão: %s  |  Modo: %s  |  RTT: %dms  |  Blocos: %d  |  Processamento: %d CU",
		sessPT, mode, s.RttMs, s.BlocksProcessed, cu,
	)
}

var sessionStatePT = map[string]string{
	"CLOSED":           "FECHADA",
	"CONNECTING":       "CONECTANDO",
	"CONNECTED_UNAUTH": "CONECTADO (S/ AUT.)",
	"IDLE":             "OCIOSA",
	"IN_POOL":          "NO POOL",
	"PAUSE_DRAINING":   "DRENANDO PAUSA",
	"PAUSED":           "PAUSADA",
	"DRAINING":         "DRENANDO",
	"QUARANTINED":      "EM QUARENTENA",
	"DISCONNECTED":     "DESCONECTADA",
}

func formatSessionStatePT(raw string) string {
	if mapped, ok := sessionStatePT[strings.ToUpper(strings.TrimSpace(raw))]; ok {
		return mapped
	}
	return raw
}

func formatMeshRolePT(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "LEADER":
		return "LÍDER"
	case "PEER":
		return "PAR"
	default:
		return raw
	}
}
