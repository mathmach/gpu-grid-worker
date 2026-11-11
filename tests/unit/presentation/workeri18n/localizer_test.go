package workeri18n_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/presentation/workeri18n"
)

func TestResolveLangAndWriteErrorEnvelope(t *testing.T) {
	reqPT := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	reqPT.Header.Set("Accept-Language", "pt-BR,pt;q=0.9,en;q=0.8")
	if got := workeri18n.ResolveLang(reqPT); got != workeri18n.LangPTBR {
		t.Fatalf("expected pt-BR, got %s", got)
	}

	recPT := httptest.NewRecorder()
	workeri18n.WriteError(recPT, reqPT, nil, derr.New(derr.CodeForbidden, http.StatusForbidden, "forbidden", nil))
	var out map[string]any
	if err := json.Unmarshal(recPT.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if recPT.Code != http.StatusForbidden || out["code"] != string(derr.CodeForbidden) {
		t.Fatalf("unexpected error envelope: status=%d body=%v", recPT.Code, out)
	}

	reqWeighted := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	reqWeighted.Header.Set("Accept-Language", "en;q=0.2,pt-BR;q=0.9")
	if got := workeri18n.ResolveLang(reqWeighted); got != workeri18n.LangPTBR {
		t.Fatalf("expected RFC 9110 q-weight pt-BR (0.9) over en (0.2), got %s", got)
	}

	unsuppErr := derr.New(derr.CodeUnsupportedModelFormat, http.StatusBadRequest, "custom unsupported format detail", nil)
	_, _, unsuppEN := workeri18n.LocalizeError(unsuppErr, workeri18n.LangEN)
	if !strings.Contains(unsuppEN, "custom unsupported format detail") {
		t.Fatalf("expected custom CodeUnsupportedModelFormat message in EN, got %q", unsuppEN)
	}
}

func TestLocalizeAllWorkerDomainErrorCodesInPortugueseAndEnglish(t *testing.T) {
	allCodes := []derr.Code{
		derr.CodeValidation,
		derr.CodeInvalidCredentials,
		derr.CodeUnauthorized,
		derr.CodeForbidden,
		derr.CodeIllegalState,
		derr.CodeConflict,
		derr.CodeRateLimited,
		derr.CodeCircuitOpen,
		derr.CodeUpstreamFailure,
		derr.CodeNoVulkanGPU,
		derr.CodeDigestMismatch,
		derr.CodePathTraversal,
		derr.CodeUnsupportedModelFormat,
		derr.CodeMethodNotAllowed,
		derr.CodeNotFound,
		derr.CodeInternal,
	}
	for _, code := range allCodes {
		appErr := derr.New(code, http.StatusUnprocessableEntity, "", nil)
		stPT, gotCodePT, msgPT := workeri18n.LocalizeError(appErr, workeri18n.LangPTBR)
		stEN, gotCodeEN, msgEN := workeri18n.LocalizeError(appErr, workeri18n.LangEN)
		if stPT != http.StatusUnprocessableEntity || stEN != http.StatusUnprocessableEntity {
			t.Fatalf("unexpected status for %s: pt=%d en=%d", code, stPT, stEN)
		}
		if gotCodePT != string(code) || gotCodeEN != string(code) {
			t.Fatalf("unexpected code echo for %s: pt=%s en=%s", code, gotCodePT, gotCodeEN)
		}
		if msgPT == "" || msgEN == "" || msgPT == msgEN {
			t.Fatalf("invalid localized copy for %s: pt=%q en=%q", code, msgPT, msgEN)
		}
	}
}
