package workeri18n

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	"golang.org/x/text/language"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

const (
	LangPTBR = "pt-BR"
	LangEN   = "en"
)

type msgPair struct {
	EN string
	PT string
}

var errorCatalog = map[derr.Code]msgPair{
	derr.CodeValidation:             {EN: "Invalid request parameters.", PT: "Parâmetros da requisição inválidos."},
	derr.CodeInvalidCredentials:     {EN: "Invalid authentication credentials.", PT: "Credenciais de autenticação inválidas."},
	derr.CodeUnauthorized:           {EN: "Authentication required.", PT: "Autenticação obrigatória."},
	derr.CodeForbidden:              {EN: "You do not have permission to perform this operation.", PT: "Você não tem permissão para realizar esta operação."},
	derr.CodeIllegalState:           {EN: "Operation not permitted in current resource state.", PT: "Operação não permitida no estado atual do recurso."},
	derr.CodeConflict:               {EN: "Operation conflicts with current worker state.", PT: "A operação conflita com o estado atual do Worker."},
	derr.CodeRateLimited:            {EN: "Too many requests. Please wait and retry.", PT: "Muitas requisições. Aguarde alguns instantes e tente novamente."},
	derr.CodeCircuitOpen:            {EN: "Upstream service circuit is temporarily open.", PT: "Circuito do serviço externo temporariamente aberto."},
	derr.CodeUpstreamFailure:        {EN: "Upstream gateway or inference engine failure.", PT: "Falha de comunicação com o Gateway ou motor de inferência."},
	derr.CodeNoVulkanGPU:            {EN: "No compatible Vulkan GPU was detected on this host.", PT: "Nenhuma GPU compatível com Vulkan foi detectada neste host."},
	derr.CodeDigestMismatch:         {EN: "Archive SHA-256 integrity verification failed.", PT: "Falha na verificação de integridade SHA-256 do pacote."},
	derr.CodePathTraversal:          {EN: "Unsafe archive path rejected.", PT: "Caminho de arquivo inseguro rejeitado."},
	derr.CodeUnsupportedModelFormat: {EN: "Unsupported model weight format for the selected inference backend.", PT: "Formato de pesos do modelo não suportado pelo motor de inferência selecionado."},
	derr.CodeMethodNotAllowed:       {EN: "HTTP method not allowed for this endpoint.", PT: "Método HTTP não permitido para este endpoint."},
	derr.CodeNotFound:               {EN: "Requested resource was not found.", PT: "Recurso solicitado não encontrado."},
	derr.CodeInternal:               {EN: "Internal worker error.", PT: "Erro interno do Worker."},
}

func normalizeLang(raw string) string {
	clean := strings.ReplaceAll(strings.TrimSpace(raw), "_", "-")
	if clean == "" {
		return ""
	}
	tags, _, err := language.ParseAcceptLanguage(clean)
	if err != nil {
		return ""
	}
	for _, tag := range tags {
		base, _ := tag.Base()
		switch base.String() {
		case "pt":
			return LangPTBR
		case "en":
			return LangEN
		}
	}
	return ""
}

func ResolveLang(req *http.Request) string {
	if req == nil {
		return LangEN
	}
	var cookieLang string
	if c, err := req.Cookie("grid_lang"); err == nil {
		cookieLang = c.Value
	}
	return cmp.Or(
		normalizeLang(req.Header.Get("X-Language")),
		normalizeLang(req.URL.Query().Get("lang")),
		normalizeLang(cookieLang),
		normalizeLang(req.Header.Get("Accept-Language")),
		LangEN,
	)
}

func LocalizeError(err error, lang string) (int, string, string) {
	if err == nil {
		return http.StatusOK, "", ""
	}
	normLang := cmp.Or(normalizeLang(lang), LangEN)
	appErr, ok := derr.AsAppError(err)
	if !ok {
		pair := errorCatalog[derr.CodeInternal]
		if normLang == LangPTBR {
			return http.StatusInternalServerError, string(derr.CodeInternal), pair.PT
		}
		return http.StatusInternalServerError, string(derr.CodeInternal), pair.EN
	}
	status := derr.StatusOf(err)
	msg := formatCatalogMessage(appErr, normLang)
	return status, string(appErr.Code), msg
}

func formatCatalogMessage(appErr *derr.AppError, lang string) string {
	if appErr.Code == derr.CodeIllegalState && len(appErr.Params) > 0 {
		if specific := formatIllegalStateParam(appErr.Params, lang); specific != "" {
			return specific
		}
	}
	pair, exists := errorCatalog[appErr.Code]
	if !exists {
		if lang == LangPTBR {
			return errorCatalog[derr.CodeInternal].PT
		}
		return errorCatalog[derr.CodeInternal].EN
	}
	if lang == LangEN {
		if appErr.Code == derr.CodeValidation || appErr.Code == derr.CodeConflict || (appErr.Code == derr.CodeUnsupportedModelFormat && len(appErr.Params) == 0) {
			return cmp.Or(appErr.Message, pair.EN)
		}
		return pair.EN
	}
	return pair.PT
}

func formatIllegalStateParam(params map[string]any, lang string) string {
	from := paramString(params, "from")
	to := paramString(params, "to")
	if from != "" && to != "" {
		if lang == LangEN {
			return fmt.Sprintf("State transition from %s to %s is not permitted.", from, to)
		}
		return fmt.Sprintf("A transição de estado de %s para %s não é permitida.", from, to)
	}
	st := cmp.Or(paramString(params, "status"), paramString(params, "state"))
	if st != "" {
		if lang == LangEN {
			return fmt.Sprintf("Operation not permitted in current resource state (%s).", st)
		}
		return fmt.Sprintf("Operação não permitida no estado atual do recurso (%s).", st)
	}
	return ""
}

func paramString(params map[string]any, key string) string {
	s, _ := params[key].(string)
	return strings.TrimSpace(s)
}

func WriteJSON(rw http.ResponseWriter, logger *slog.Logger, status int, payload any) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.WriteHeader(status)
	if err := json.NewEncoder(rw).Encode(payload); err != nil && logger != nil {
		logger.Warn("http_json_encode_failed", "error", err.Error())
	}
}

func WriteError(rw http.ResponseWriter, req *http.Request, logger *slog.Logger, err error) {
	WriteErrorWithExtra(rw, req, logger, err, nil)
}

func WriteErrorWithExtra(rw http.ResponseWriter, req *http.Request, logger *slog.Logger, err error, extra map[string]any) {
	status, code, msg := LocalizeError(err, ResolveLang(req))
	if status >= 500 && logger != nil && err != nil {
		logger.Error("http_server_error", "code", code, "status", status, "error", err.Error())
	}
	out := make(map[string]any, len(extra)+2)
	maps.Copy(out, extra)
	out["error"] = msg
	out["code"] = code
	WriteJSON(rw, logger, status, out)
}
