package workerhttp

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/presentation/workeri18n"
)

type ConfigResponse struct {
	GatewayURL        string   `json:"gatewayUrl"`
	WorkerName        string   `json:"workerName"`
	AccountEmail      string   `json:"accountEmail,omitempty"`
	HasWorkerToken    bool     `json:"hasWorkerToken"`
	SelectedIDs       []string `json:"selectedIds"`
	ScheduleMode      string   `json:"scheduleMode"`
	IdleThresholdSec  int      `json:"idleThresholdSec"`
	ManualPaused      bool     `json:"manualPaused"`
	PauseDraining     bool     `json:"pauseDraining"`
	ModelMode         string   `json:"modelMode"`
	SelectedModels    []string `json:"selectedModels"`
	EnabledBackends   []string `json:"enabledBackends"`
	SupportedBackends []string `json:"supportedBackends"`
	SoloEngineStatus  string   `json:"soloEngineStatus"`
	SoloModelID       string   `json:"soloModelId"`
	Language          string   `json:"language"`
	UIMode            string   `json:"uiMode"`
}

type configPatch struct {
	GatewayURL         *string   `json:"gatewayUrl"`
	WorkerToken        *string   `json:"workerToken"`
	WorkerName         *string   `json:"workerName"`
	SelectedIDs        *[]string `json:"selectedIds"`
	ScheduleMode       *string   `json:"scheduleMode"`
	IdleThresholdSec   *int      `json:"idleThresholdSec"`
	ManualPaused       *bool     `json:"manualPaused"`
	ModelMode          *string   `json:"modelMode"`
	ModelSelectionMode *string   `json:"modelSelectionMode"`
	SelectedModels     *[]string `json:"selectedModels"`
	EnabledBackends    *[]string `json:"enabledBackends"`
	Language           *string   `json:"language"`
	UIMode             *string   `json:"uiMode"`
}

func (h *Handler) handleUpdateConfig(rw http.ResponseWriter, req *http.Request) {
	if !hasJSONContentType(req.Header.Get("Content-Type")) {
		workeri18n.WriteError(rw, req, h.logger, derr.New(derr.CodeValidation, http.StatusUnsupportedMediaType, "Content-Type application/json is required", nil))
		return
	}
	patch, err := decodeConfigPatch(rw, req)
	if err != nil {
		workeri18n.WriteError(rw, req, h.logger, err)
		return
	}
	upd, err := buildUpdateSettings(patch)
	if err != nil {
		workeri18n.WriteError(rw, req, h.logger, err)
		return
	}
	if err := h.eng.ApplySettings(upd); err != nil {
		workeri18n.WriteError(rw, req, h.logger, err)
		return
	}
	workeri18n.WriteJSON(rw, h.logger, http.StatusOK, h.buildConfigResponse(h.eng.Snapshot()))
}

func hasJSONContentType(raw string) bool {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func decodeConfigPatch(rw http.ResponseWriter, req *http.Request) (configPatch, error) {
	var patch configPatch
	if req.Body == nil {
		return patch, derr.New(derr.CodeValidation, http.StatusBadRequest, "request JSON body is required", nil)
	}
	bounded := http.MaxBytesReader(rw, req.Body, 64<<10)
	dec := json.NewDecoder(bounded)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&patch); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return patch, derr.New(derr.CodeValidation, http.StatusRequestEntityTooLarge, "configuration payload exceeds 64 KiB limit", nil)
		}
		return patch, derr.New(derr.CodeValidation, http.StatusBadRequest, "invalid configuration JSON payload", nil)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return patch, derr.New(derr.CodeValidation, http.StatusBadRequest, "unexpected trailing JSON data", nil)
	}
	if err := validateConfigPatchNumbers(patch); err != nil {
		return patch, err
	}
	return patch, nil
}

func validateConfigPatchNumbers(patch configPatch) error {
	if patch.IdleThresholdSec != nil {
		sec := *patch.IdleThresholdSec
		if sec < worker.MinIdleThresholdSec || sec > worker.MaxIdleThresholdSec {
			return derr.New(derr.CodeValidation, http.StatusBadRequest, "idleThresholdSec must be between 15 and 3600", nil)
		}
	}
	return nil
}

func buildUpdateSettings(patch configPatch) (worker.UpdateSettings, error) {
	var upd worker.UpdateSettings
	if err := applyPatchIdentity(&upd, patch); err != nil {
		return upd, err
	}
	applyPatchSchedule(&upd, patch)
	if err := applyPatchAppearance(&upd, patch); err != nil {
		return upd, err
	}
	if err := applyPatchModelAndBackends(&upd, patch); err != nil {
		return upd, err
	}
	return upd, nil
}

func applyPatchIdentity(upd *worker.UpdateSettings, patch configPatch) error {
	if patch.GatewayURL != nil {
		normGW, err := nodecore.NormalizeGatewayURL(*patch.GatewayURL)
		if err != nil {
			return err
		}
		upd.GatewayURLSet = true
		upd.GatewayURL = normGW
	}
	if patch.WorkerToken != nil {
		upd.WorkerTokenSet = true
		upd.WorkerToken = *patch.WorkerToken
	}
	if patch.WorkerName != nil {
		upd.WorkerNameSet = true
		upd.WorkerName = *patch.WorkerName
	}
	if patch.SelectedIDs != nil {
		upd.SelectedIDsSet = true
		upd.SelectedIDs = append([]string(nil), (*patch.SelectedIDs)...)
	}
	return nil
}

func applyPatchSchedule(upd *worker.UpdateSettings, patch configPatch) {
	if patch.ScheduleMode != nil {
		upd.ScheduleModeSet = true
		upd.ScheduleMode = *patch.ScheduleMode
	}
	if patch.IdleThresholdSec != nil {
		upd.IdleThresholdSet = true
		upd.IdleThresholdSec = *patch.IdleThresholdSec
	}
	upd.ManualPaused = patch.ManualPaused
}

func applyPatchAppearance(upd *worker.UpdateSettings, patch configPatch) error {
	if patch.Language != nil {
		parsedLang, err := nodecore.ParseWorkerLanguage(*patch.Language)
		if err != nil {
			return err
		}
		upd.LanguageSet = true
		upd.Language = parsedLang
	}
	if patch.UIMode != nil {
		parsedMode, err := nodecore.ParseWorkerUIMode(*patch.UIMode)
		if err != nil {
			return err
		}
		upd.UIModeSet = true
		upd.UIMode = string(parsedMode)
	}
	return nil
}

func applyPatchModelAndBackends(upd *worker.UpdateSettings, patch configPatch) error {
	rawMode, err := resolvePatchedModelMode(patch.ModelMode, patch.ModelSelectionMode)
	if err != nil {
		return err
	}
	if rawMode != nil {
		parsedRole, err := nodecore.ParseServingRole(*rawMode)
		if err != nil {
			return err
		}
		upd.ModelModeSet = true
		upd.ModelMode = string(parsedRole)
	}
	if patch.SelectedModels != nil {
		upd.SelectedModelsSet = true
		upd.SelectedModels = append([]string(nil), (*patch.SelectedModels)...)
	}
	if patch.EnabledBackends != nil {
		validated := make([]string, 0, len(*patch.EnabledBackends))
		for _, raw := range *patch.EnabledBackends {
			parsed, err := nodecore.ParseEngineBackend(raw)
			if err != nil {
				return err
			}
			validated = append(validated, parsed.String())
		}
		upd.BackendsSet = true
		upd.EnabledBackends = validated
	}
	return nil
}

func resolvePatchedModelMode(primary, alias *string) (*string, error) {
	if primary == nil {
		return alias, nil
	}
	if alias == nil {
		return primary, nil
	}
	if !strings.EqualFold(strings.TrimSpace(*primary), strings.TrimSpace(*alias)) {
		return nil, derr.New(derr.CodeValidation, http.StatusBadRequest, "conflicting modelMode and modelSelectionMode values", nil)
	}
	return primary, nil
}

func (h *Handler) buildConfigResponse(snap worker.StatusSnapshot) ConfigResponse {
	soloStatus, soloModel := deriveSoloState(snap)
	return ConfigResponse{
		GatewayURL:        snap.GatewayURL,
		WorkerName:        snap.WorkerName,
		AccountEmail:      snap.AccountEmail,
		HasWorkerToken:    strings.TrimSpace(snap.WorkerToken) != "",
		SelectedIDs:       nonNilStrings(snap.SelectedIDs),
		ScheduleMode:      snap.ScheduleMode,
		IdleThresholdSec:  snap.IdleThresholdSec,
		ManualPaused:      snap.ManualPaused,
		PauseDraining:     snap.PauseDraining || isPauseDraining(snap.Status),
		ModelMode:         snap.ModelMode,
		SelectedModels:    nonNilStrings(snap.SelectedModels),
		EnabledBackends:   nonNilStrings(snap.EnabledBackends),
		SupportedBackends: nonNilStrings(snap.SupportedBackends),
		SoloEngineStatus:  soloStatus,
		SoloModelID:       soloModel,
		Language:          snap.Language,
		UIMode:            snap.UIMode,
	}
}
