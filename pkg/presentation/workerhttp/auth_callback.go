package workerhttp

import (
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/presentation/workeri18n"
)

const maxCallbackQueryBytes = 2048

func (h *Handler) handleBrowserAuthCallback(rw http.ResponseWriter, req *http.Request) {
	if !isAllowedLoopbackHost(req.Host) {
		workeri18n.WriteError(rw, req, h.logger, derr.New(derr.CodeForbidden, http.StatusForbidden, "loopback Host header is required", nil))
		return
	}
	if req.Method != http.MethodGet {
		workeri18n.WriteError(rw, req, h.logger, derr.New(derr.CodeMethodNotAllowed, http.StatusMethodNotAllowed, "HTTP GET required", nil))
		return
	}
	if len(req.URL.RawQuery) > maxCallbackQueryBytes {
		workeri18n.WriteError(rw, req, h.logger, derr.New(derr.CodeValidation, http.StatusBadRequest, "callback query string exceeds maximum length", nil))
		return
	}
	if reason := validateCallbackNavigationHeaders(req); reason != "" {
		workeri18n.WriteError(rw, req, h.logger, derr.New(derr.CodeForbidden, http.StatusForbidden, reason, nil))
		return
	}

	lang := h.eng.Snapshot().Language
	code := strings.TrimSpace(req.URL.Query().Get("code"))
	state := strings.TrimSpace(req.URL.Query().Get("state"))
	snap, err := h.eng.CompleteBrowserLogin(req.Context(), code, state)
	if err != nil {
		status, msg := localizeAuthCallbackError(err, lang)
		writeCallbackHTML(rw, h.logger, status, buildAuthCallbackErrorHTML(lang, msg))
		return
	}

	if h.focusCh != nil {
		select {
		case h.focusCh <- struct{}{}:
		default:
		}
	}
	writeCallbackHTML(rw, h.logger, http.StatusOK, buildAuthCallbackSuccessHTML(snap.Language, snap.AccountEmail, snap.GatewayURL))
}

func validateCallbackNavigationHeaders(req *http.Request) string {
	dest := strings.ToLower(strings.TrimSpace(req.Header.Get("Sec-Fetch-Dest")))
	if dest != "" && dest != "document" {
		return "top-level document navigation is required"
	}
	mode := strings.ToLower(strings.TrimSpace(req.Header.Get("Sec-Fetch-Mode")))
	if mode != "" && mode != "navigate" {
		return "browser navigation mode is required"
	}
	return ""
}

var callbackErrorPT = map[string]string{
	"missing authorization code or state":                "Código de autorização ou estado OAuth ausente.",
	"browser login session expired or not started":       "Sessão de login no navegador expirada ou não iniciada.",
	"invalid OAuth state parameter":                      "Parâmetro de estado OAuth inválido.",
	"stop the worker before changing the linked account": "Pare o Worker antes de trocar a conta vinculada.",
	"worker portal port is not configured":               "A porta de autenticação com o Gateway não está configurada.",
}

func localizeAuthCallbackError(err error, lang string) (int, string) {
	status, _, msg := workeri18n.LocalizeError(err, lang)
	if lang != "pt-BR" {
		return status, msg
	}
	if appErr, ok := derr.AsAppError(err); ok {
		if mapped, found := callbackErrorPT[strings.TrimSpace(appErr.Message)]; found {
			return status, mapped
		}
	}
	return status, msg
}

func writeCallbackHTML(rw http.ResponseWriter, logger interface{ Debug(string, ...any) }, status int, doc string) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("X-Frame-Options", "DENY")
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	rw.WriteHeader(status)
	if _, err := rw.Write([]byte(doc)); err != nil && logger != nil {
		logger.Debug("auth_callback_html_write_failed", "error", err.Error())
	}
}

func buildAuthCallbackSuccessHTML(lang, email, gatewayURL string) string {
	safeEmail := html.EscapeString(strings.TrimSpace(email))
	dashHref := html.EscapeString(strings.TrimRight(strings.TrimSpace(gatewayURL), "/") + "/?tab=dashboard")
	if lang == "pt-BR" {
		return renderCallbackCardHTML(
			"pt-BR",
			"GPU Grid Worker · Autenticado",
			"#22c55e",
			"Worker autenticado",
			fmt.Sprintf("A conta <strong>%s</strong> foi vinculada ao seu GPU Grid Worker. Você já pode fechar esta aba e voltar para o aplicativo.", safeEmail),
			dashHref,
			"Voltar ao Meu Painel",
		)
	}
	return renderCallbackCardHTML(
		"en",
		"GPU Grid Worker · Authenticated",
		"#22c55e",
		"Worker authenticated",
		fmt.Sprintf("Account <strong>%s</strong> is now linked to your GPU Grid Worker. Return to the desktop app to start sharing your GPU.", safeEmail),
		dashHref,
		"Return to My Dashboard",
	)
}

func buildAuthCallbackErrorHTML(lang, errMsg string) string {
	safeMsg := html.EscapeString(strings.TrimSpace(errMsg))
	if lang == "pt-BR" {
		return renderCallbackCardHTML(
			"pt-BR",
			"Falha na Autenticação · GPU Grid Worker",
			"#ef4444",
			"Não foi possível concluir a vinculação",
			safeMsg+" Volte ao aplicativo GPU Grid Worker e clique novamente em Entrar com o Navegador.",
			"",
			"",
		)
	}
	return renderCallbackCardHTML(
		"en",
		"Authentication Failed · GPU Grid Worker",
		"#ef4444",
		"Could not complete Worker linking",
		safeMsg+" Return to the GPU Grid Worker desktop app and click Sign In with Browser again.",
		"",
		"",
	)
}

func renderCallbackCardHTML(lang, pageTitle, accentHex, heading, bodyHTML, btnHref, btnLabel string) string {
	htmlLang := "en"
	if lang == "pt-BR" {
		htmlLang = "pt-BR"
	}
	actionHTML := ""
	if btnHref != "" && btnLabel != "" {
		actionHTML = fmt.Sprintf(`<a class="btn" href="%s">%s</a>`, btnHref, html.EscapeString(btnLabel))
	}
	return fmt.Sprintf(`<!doctype html>
<html lang="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0f1115;color:#f3f4f6;font-family:system-ui,-apple-system,sans-serif;padding:24px}
.card{max-width:460px;width:100%%;background:#161920;border:1px solid #262b36;border-radius:14px;padding:28px;box-sizing:border-box}
.dot{display:inline-block;width:10px;height:10px;border-radius:50%%;background:%s;margin-right:8px}
h1{font-size:19px;margin:0 0 12px;display:flex;align-items:center}
p{font-size:14px;line-height:1.55;color:#cbd5e1;margin:0 0 20px}
.btn{display:inline-block;background:#2563eb;color:#fff;text-decoration:none;font-weight:600;font-size:13px;padding:10px 16px;border-radius:8px}
</style>
</head>
<body>
<div class="card">
<h1><span class="dot"></span>%s</h1>
<p>%s</p>
%s
</div>
</body>
</html>`, htmlLang, html.EscapeString(pageTitle), accentHex, html.EscapeString(heading), bodyHTML, actionHTML)
}
