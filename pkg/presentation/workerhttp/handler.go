package workerhttp

import (
	"cmp"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/presentation/workeri18n"
)

const LocalIPCAuthHeader = "X-Grid-Worker-Local"

func CheckAlreadyRunning(controlAddr string) (bool, int) {
	baseURL := "http://" + strings.TrimSpace(controlAddr)
	client := &http.Client{Timeout: 250 * time.Millisecond}
	resp, err := client.Get(baseURL + "/api/ping")
	if err != nil {
		return false, 0
	}
	defer resp.Body.Close()

	var out struct {
		Status    string `json:"status"`
		PID       int    `json:"pid"`
		MachineID string `json:"machineId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, 0
	}
	if out.Status != "running" || out.PID <= 0 || !strings.HasPrefix(strings.TrimSpace(out.MachineID), "m-") {
		return false, 0
	}
	notifyFocusBestEffort(client, baseURL)
	return true, out.PID
}

func notifyFocusBestEffort(client *http.Client, baseURL string) {
	focusReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/focus", nil)
	if err != nil {
		return
	}
	focusReq.Header.Set(LocalIPCAuthHeader, "1")
	focusResp, err := client.Do(focusReq)
	if err != nil {
		return
	}
	if closeErr := focusResp.Body.Close(); closeErr != nil {
		return
	}
}

type statusResponse struct {
	worker.StatusSnapshot
	EnabledBackends []string `json:"enabledBackends"`
}

type Handler struct {
	eng     *worker.Engine
	focusCh chan<- struct{}
	logger  *slog.Logger
}

func NewHandler(eng *worker.Engine, focusCh chan<- struct{}, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	h := &Handler{eng: eng, focusCh: focusCh, logger: logger}
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/api/ping", h.handlePing)
	apiMux.HandleFunc("/api/focus", h.handleFocus)
	apiMux.HandleFunc("/api/status", h.handleStatus)
	apiMux.HandleFunc("/api/config", h.handleConfig)

	rootMux := http.NewServeMux()
	rootMux.HandleFunc("/auth/callback", h.handleBrowserAuthCallback)
	rootMux.Handle("/", h.loopbackSecurityGuard(apiMux))
	return rootMux
}

func (h *Handler) loopbackSecurityGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if reason := validateLoopbackIPCRequest(req); reason != "" {
			workeri18n.WriteError(rw, req, h.logger, derr.New(derr.CodeForbidden, http.StatusForbidden, reason, nil))
			return
		}
		next.ServeHTTP(rw, req)
	})
}

func validateLoopbackIPCRequest(req *http.Request) string {
	if !isAllowedLoopbackHost(req.Host) {
		return "loopback Host header is required"
	}
	if strings.TrimSpace(req.Header.Get("Origin")) != "" {
		return "browser Origin header rejected on loopback IPC"
	}
	secSite := strings.ToLower(strings.TrimSpace(req.Header.Get("Sec-Fetch-Site")))
	if secSite != "" && secSite != "none" {
		return "browser Fetch-Metadata rejected on loopback IPC"
	}
	if isMutatingHTTPMethod(req.Method) && strings.TrimSpace(req.Header.Get(LocalIPCAuthHeader)) != "1" {
		return "X-Grid-Worker-Local: 1 header is required for state mutations"
	}
	return ""
}

func isAllowedLoopbackHost(rawHost string) bool {
	host := strings.TrimSpace(rawHost)
	if host == "" {
		return false
	}
	if parsedHost, portStr, err := net.SplitHostPort(host); err == nil {
		portNum, pErr := strconv.Atoi(portStr)
		if pErr != nil || portNum < 1 || portNum > 65535 {
			return false
		}
		host = parsedHost
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	return host == "127.0.0.1" || host == "::1" || strings.EqualFold(host, "localhost")
}

func isMutatingHTTPMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func (h *Handler) handlePing(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		workeri18n.WriteError(rw, req, h.logger, derr.New(derr.CodeMethodNotAllowed, http.StatusMethodNotAllowed, "HTTP GET required", nil))
		return
	}
	workeri18n.WriteJSON(rw, h.logger, http.StatusOK, map[string]any{
		"status":    "running",
		"pid":       os.Getpid(),
		"machineId": h.eng.MachineID(),
	})
}

func (h *Handler) handleFocus(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		workeri18n.WriteError(rw, req, h.logger, derr.New(derr.CodeMethodNotAllowed, http.StatusMethodNotAllowed, "HTTP POST required", nil))
		return
	}
	if h.focusCh != nil {
		select {
		case h.focusCh <- struct{}{}:
		default:
		}
	}
	workeri18n.WriteJSON(rw, h.logger, http.StatusOK, map[string]string{"status": "focused"})
}

func (h *Handler) handleStatus(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		workeri18n.WriteError(rw, req, h.logger, derr.New(derr.CodeMethodNotAllowed, http.StatusMethodNotAllowed, "HTTP GET required", nil))
		return
	}
	workeri18n.WriteJSON(rw, h.logger, http.StatusOK, h.buildStatusResponse(h.eng.Snapshot()))
}

func (h *Handler) handleConfig(rw http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet, http.MethodHead:
		workeri18n.WriteJSON(rw, h.logger, http.StatusOK, h.buildConfigResponse(h.eng.Snapshot()))
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		h.handleUpdateConfig(rw, req)
	default:
		workeri18n.WriteError(rw, req, h.logger, derr.New(derr.CodeMethodNotAllowed, http.StatusMethodNotAllowed, "HTTP GET, POST, PUT, or PATCH required", nil))
	}
}

func (h *Handler) buildStatusResponse(snap worker.StatusSnapshot) statusResponse {
	soloStatus, soloModel := deriveSoloState(snap)
	snap.SoloEngineStatus = soloStatus
	snap.SoloModelID = soloModel
	snap.PauseDraining = snap.PauseDraining || isPauseDraining(snap.Status)
	return statusResponse{
		StatusSnapshot:  snap,
		EnabledBackends: nonNilStrings(snap.EnabledBackends),
	}
}

func deriveSoloState(snap worker.StatusSnapshot) (string, string) {
	soloSt := strings.ToUpper(strings.TrimSpace(snap.SoloEngineStatus))
	if soloSt != "" && soloSt != worker.SoloStatusIdle {
		return soloSt, cmp.Or(snap.SoloModelID, snap.ActiveModelID)
	}
	st := strings.ToUpper(strings.TrimSpace(snap.Status))
	if st == worker.SoloStatusPulling || st == worker.SoloStatusStarting || st == worker.SoloStatusReady {
		return st, cmp.Or(snap.SoloModelID, snap.ActiveModelID)
	}
	if strings.EqualFold(snap.ExecutionMode, "SOLO") {
		return worker.SoloStatusReady, cmp.Or(snap.SoloModelID, snap.ActiveModelID)
	}
	return worker.SoloStatusIdle, ""
}

func isPauseDraining(status string) bool {
	st := strings.ToUpper(strings.TrimSpace(status))
	return st == worker.StatusPauseDraining || st == "DRAINING"
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
