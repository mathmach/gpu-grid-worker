package workerconfig

import (
	"cmp"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

type WorkerConfig struct {
	GatewayURL              string
	EmbeddedGatewayURL      string
	BootstrapURLs           []string
	BootstrapURLsExplicit   bool
	BootstrapPublicKeys     []string
	Token                   string
	WorkerName              string
	Devices                 []string
	ModelMode               string
	SelectedModels          []string
	CDNModelsBaseURL        string
	InsecureWS              bool
	EnabledBackends         []string
	EnabledBackendsExplicit bool
	AutoStart               bool
	Headless                bool
	NoBrowser               bool
	ListDevices             bool
	ControlAddr             string
	ControlPort             int
	ScheduleMode            string
	IdleThresholdSec        int
	SoloEngineBootTimeout   time.Duration
	DataDir                 string
	ConfigPath              string
	DefaultLanguage         string
	HasDesktopDisplay       bool
}

type workerEnvSpec struct {
	GatewayURL          string
	BootstrapURLs       string
	GridWorkerToken     string
	FallbackToken       string
	WorkerName          string
	Devices             string
	ModelMode           string
	Models              string
	WorkerBackends      string
	FallbackBackends    string
	WorkerCDNModels     string
	FallbackCDN         string
	WorkerInsecureWS    *bool
	FallbackInsecure    bool
	ScheduleMode        string
	IdleThresholdSec    *int
	SoloBootTimeoutSec  *int
	FallbackSoloBootSec *int
	DataDir             string
	ConfigPath          string
	GridLang            string
	LCAll               string
	LCMessages          string
	PosixLang           string
}

func readWorkerEnvSpec(lookup func(string) (string, bool)) (workerEnvSpec, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	getStr := func(k string) string {
		v, _ := lookup(k)
		return v
	}
	workerInsecure, err := lookupOptionalBool(lookup, "GPU_GRID_INSECURE_WS")
	if err != nil {
		return workerEnvSpec{}, err
	}
	fallbackInsecurePtr, err := lookupOptionalBool(lookup, "INSECURE_WS")
	if err != nil {
		return workerEnvSpec{}, err
	}
	idleSec, err := lookupOptionalInt(lookup, "GPU_GRID_IDLE_SEC")
	if err != nil {
		return workerEnvSpec{}, err
	}
	soloBoot, err := lookupOptionalInt(lookup, "GPU_GRID_SOLO_BOOT_TIMEOUT_SEC")
	if err != nil {
		return workerEnvSpec{}, err
	}
	fallbackSoloBoot, err := lookupOptionalInt(lookup, "SOLO_ENGINE_BOOT_TIMEOUT_SEC")
	if err != nil {
		return workerEnvSpec{}, err
	}
	return workerEnvSpec{
		GatewayURL:          getStr("GPU_GRID_GATEWAY"),
		BootstrapURLs:       getStr("GPU_GRID_BOOTSTRAP_URL"),
		GridWorkerToken:     getStr("GRID_WORKER_TOKEN"),
		FallbackToken:       getStr("GPU_GRID_TOKEN"),
		WorkerName:          getStr("GPU_GRID_NAME"),
		Devices:             getStr("GPU_GRID_DEVICES"),
		ModelMode:           getStr("GPU_GRID_MODEL_MODE"),
		Models:              getStr("GPU_GRID_MODELS"),
		WorkerBackends:      getStr("GPU_GRID_BACKENDS"),
		FallbackBackends:    getStr("ENABLED_BACKENDS"),
		WorkerCDNModels:     getStr("GPU_GRID_CDN_MODELS_URL"),
		FallbackCDN:         getStr("CDN_MODELS_BASE_URL"),
		WorkerInsecureWS:    workerInsecure,
		FallbackInsecure:    derefBoolOr(fallbackInsecurePtr, false),
		ScheduleMode:        getStr("GPU_GRID_SCHEDULE_MODE"),
		IdleThresholdSec:    idleSec,
		SoloBootTimeoutSec:  soloBoot,
		FallbackSoloBootSec: fallbackSoloBoot,
		DataDir:             getStr("GPU_GRID_DATA_DIR"),
		ConfigPath:          getStr("GPU_GRID_CONFIG"),
		GridLang:            getStr("GPU_GRID_LANG"),
		LCAll:               getStr("LC_ALL"),
		LCMessages:          getStr("LC_MESSAGES"),
		PosixLang:           getStr("LANG"),
	}, nil
}

func lookupOptionalBool(lookup func(string) (string, bool), key string) (*bool, error) {
	raw, ok := lookup(key)
	clean := strings.TrimSpace(raw)
	if !ok || clean == "" {
		return nil, nil
	}
	v, err := parseFlexibleBool(clean)
	if err != nil {
		return nil, derr.Wrap(derr.CodeValidation, 400, "invalid environment variable value", err)
	}
	return &v, nil
}

func lookupOptionalInt(lookup func(string) (string, bool), key string) (*int, error) {
	raw, ok := lookup(key)
	clean := strings.TrimSpace(raw)
	if !ok || clean == "" {
		return nil, nil
	}
	v, err := strconv.Atoi(clean)
	if err != nil {
		return nil, derr.Wrap(derr.CodeValidation, 400, "invalid environment variable value", err)
	}
	return &v, nil
}

func parseFlexibleBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return strconv.ParseBool(strings.TrimSpace(v))
	}
}

func LoadWorkerConfig(args []string) (WorkerConfig, error) {
	if err := ValidateWorkerEnv(os.LookupEnv); err != nil {
		return WorkerConfig{}, err
	}
	spec, err := readWorkerEnvSpec(os.LookupEnv)
	if err != nil {
		return WorkerConfig{}, err
	}
	cfg := defaultWorkerConfig(args, spec)

	var devsRaw string
	var modelsRaw string
	var backendsRaw string
	var bootstrapRaw string
	prevDataDir := cfg.DataDir
	prevConfigPath := cfg.ConfigPath
	soloBootSec := int(cfg.SoloEngineBootTimeout / time.Second)
	defaultBackendsCSV := strings.Join(cfg.EnabledBackends, ",")
	fs := flag.NewFlagSet("gpu-worker", flag.ContinueOnError)
	fs.StringVar(&cfg.GatewayURL, "gateway", cfg.GatewayURL, "gateway HTTP URL")
	fs.StringVar(&bootstrapRaw, "bootstrap-url", strings.TrimSpace(spec.BootstrapURLs), "comma-separated signed network bootstrap manifest URLs")
	fs.StringVar(&cfg.Token, "token", cfg.Token, "account worker token (wk_...)")
	fs.StringVar(&cfg.WorkerName, "name", cfg.WorkerName, "worker machine label")
	fs.StringVar(&devsRaw, "devices", strings.TrimSpace(spec.Devices), "comma-separated Vulkan GPU IDs or 'all'")
	fs.StringVar(&cfg.ModelMode, "model-mode", strings.TrimSpace(spec.ModelMode), "model selection mode: auto or multi")
	fs.StringVar(&modelsRaw, "models", strings.TrimSpace(spec.Models), "comma-separated model IDs for multi mode")
	fs.StringVar(&backendsRaw, "backends", defaultBackendsCSV, "comma-separated enabled inference backends")
	fs.StringVar(&backendsRaw, "enabled-backends", defaultBackendsCSV, "alias for --backends")
	fs.StringVar(&cfg.CDNModelsBaseURL, "cdn-models-url", cfg.CDNModelsBaseURL, "CDN base URL for CAS model pulls")
	fs.BoolVar(&cfg.InsecureWS, "insecure-ws", cfg.InsecureWS, "allow plaintext ws:// tunnel to public gateway hosts")
	fs.StringVar(&cfg.ScheduleMode, "mode", cfg.ScheduleMode, "schedule mode: always or idle")
	fs.StringVar(&cfg.ScheduleMode, "schedule", cfg.ScheduleMode, "alias for --mode")
	fs.IntVar(&cfg.IdleThresholdSec, "idle-sec", cfg.IdleThresholdSec, "idle seconds before resuming in idle mode (15-3600)")
	fs.IntVar(&soloBootSec, "solo-boot-timeout-sec", soloBootSec, "SOLO engine startup timeout in seconds (5-1800)")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "worker local cache and runtime data directory")
	fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "worker persisted config.json path")
	fs.BoolVar(&cfg.AutoStart, "autostart", false, "automatically start renting GPUs on launch")
	fs.BoolVar(&cfg.Headless, "headless", false, "run in headless CLI mode without opening the desktop GUI")
	fs.BoolVar(&cfg.NoBrowser, "no-browser", false, "do not launch desktop GUI or browser")
	fs.BoolVar(&cfg.ListDevices, "list-devices", false, "probe local Vulkan GPUs and exit")
	if err := fs.Parse(args); err != nil {
		return WorkerConfig{}, err
	}
	idleFlagExplicit := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "backends", "enabled-backends":
			cfg.EnabledBackendsExplicit = true
		case "idle-sec":
			idleFlagExplicit = true
		}
	})
	cfg.SoloEngineBootTimeout = time.Duration(soloBootSec) * time.Second
	if cfg.DataDir != prevDataDir && cfg.ConfigPath == prevConfigPath {
		cfg.ConfigPath = filepath.Join(filepath.Clean(cfg.DataDir), "config.json")
	}
	if err := applyParsedWorkerFlags(&cfg, devsRaw, modelsRaw, backendsRaw, bootstrapRaw, idleFlagExplicit); err != nil {
		return WorkerConfig{}, err
	}
	return cfg, nil
}

func defaultWorkerConfig(args []string, spec workerEnvSpec) WorkerConfig {
	dataDir := resolveWorkerDataDir(spec.DataDir)
	cfgPath := cmp.Or(strings.TrimSpace(spec.ConfigPath), filepath.Join(dataDir, "config.json"))
	envTok := cmp.Or(strings.TrimSpace(spec.GridWorkerToken), strings.TrimSpace(spec.FallbackToken))
	insecureWS := spec.FallbackInsecure
	if spec.WorkerInsecureWS != nil {
		insecureWS = *spec.WorkerInsecureWS
	}
	rawEnvBackends := cmp.Or(strings.TrimSpace(spec.WorkerBackends), strings.TrimSpace(spec.FallbackBackends))
	backends := splitCSV(rawEnvBackends)
	backendsExplicit := len(backends) > 0
	if !backendsExplicit {
		backends = defaultEnabledBackends()
	}
	cdnModels := strings.TrimRight(cmp.Or(strings.TrimSpace(spec.WorkerCDNModels), strings.TrimSpace(spec.FallbackCDN)), "/")
	embedded := readCurrentExeBootstrapPayload()
	bootstrapURLs := nodecore.DefaultNetworkBootstrapURLs()
	if len(embedded.BootstrapURLs) > 0 {
		bootstrapURLs = append([]string(nil), embedded.BootstrapURLs...)
	}
	pubKeys := mergeDefaultAndEmbeddedBootstrapKeys(embedded.BootstrapPublicKeys)

	return WorkerConfig{
		GatewayURL:              strings.TrimSpace(spec.GatewayURL),
		EmbeddedGatewayURL:      embedded.GatewayURL,
		BootstrapURLs:           bootstrapURLs,
		BootstrapPublicKeys:     pubKeys,
		Token:                   envTok,
		WorkerName:              strings.TrimSpace(spec.WorkerName),
		CDNModelsBaseURL:        cdnModels,
		InsecureWS:              insecureWS,
		EnabledBackends:         backends,
		EnabledBackendsExplicit: backendsExplicit,
		ScheduleMode:            strings.TrimSpace(spec.ScheduleMode),
		IdleThresholdSec:        derefIntOr(spec.IdleThresholdSec, 0),
		SoloEngineBootTimeout:   resolveWorkerSoloBootTimeout(spec),
		ControlAddr:             "127.0.0.1:17771",
		ControlPort:             nodecore.DefaultWorkerControlPort,
		DataDir:                 dataDir,
		ConfigPath:              cfgPath,
		DefaultLanguage:         detectSystemLanguage(spec),
		HasDesktopDisplay:       HasGraphicalDisplayFor(runtime.GOOS, os.LookupEnv, args),
	}
}

func mergeDefaultAndEmbeddedBootstrapKeys(embeddedKeys []string) []string {
	combined := make([]string, 0, 1+len(embeddedKeys))
	combined = append(combined, nodecore.DefaultNetworkBootstrapPublicKeyHex)
	combined = append(combined, embeddedKeys...)
	norm, err := nodecore.NormalizeBootstrapPublicKeyList(combined)
	if err != nil || len(norm) == 0 {
		return []string{nodecore.DefaultNetworkBootstrapPublicKeyHex}
	}
	return norm
}

func readCurrentExeBootstrapPayload() nodecore.WorkerBootstrapPayload {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return nodecore.WorkerBootstrapPayload{}
	}
	payload, ok := ReadExecutableBootstrapPayload(exe)
	if !ok {
		return nodecore.WorkerBootstrapPayload{}
	}
	return payload
}

func ReadExecutableBootstrapGatewayURL(exePath string) string {
	payload, ok := ReadExecutableBootstrapPayload(exePath)
	if !ok {
		return ""
	}
	return payload.GatewayURL
}

func ReadExecutableBootstrapPayload(exePath string) (nodecore.WorkerBootstrapPayload, bool) {
	f, err := os.Open(strings.TrimSpace(exePath))
	if err != nil {
		return nodecore.WorkerBootstrapPayload{}, false
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.IsDir() || st.Size() < 46 {
		return nodecore.WorkerBootstrapPayload{}, false
	}
	readLen := min(st.Size(), int64(nodecore.MaxWorkerBootstrapTailBytes))
	buf := make([]byte, readLen)
	n, err := f.ReadAt(buf, st.Size()-readLen)
	if n <= 0 || (err != nil && n < len(buf)) {
		return nodecore.WorkerBootstrapPayload{}, false
	}
	return nodecore.DecodeWorkerBootstrapPayload(buf[:n])
}

func resolveWorkerSoloBootTimeout(spec workerEnvSpec) time.Duration {
	if spec.SoloBootTimeoutSec != nil {
		return time.Duration(*spec.SoloBootTimeoutSec) * time.Second
	}
	if spec.FallbackSoloBootSec != nil {
		return time.Duration(*spec.FallbackSoloBootSec) * time.Second
	}
	return time.Duration(defaultSoloEngineBootTimeoutSec) * time.Second
}

func derefIntOr(ptr *int, def int) int {
	if ptr != nil {
		return *ptr
	}
	return def
}

func derefBoolOr(ptr *bool, def bool) bool {
	if ptr != nil {
		return *ptr
	}
	return def
}

func applyParsedWorkerFlags(cfg *WorkerConfig, devsRaw, modelsRaw, backendsRaw, bootstrapRaw string, idleFlagExplicit bool) error {
	if err := validateWorkerNumericBounds(cfg, idleFlagExplicit); err != nil {
		return err
	}
	cfg.CDNModelsBaseURL = strings.TrimRight(strings.TrimSpace(cfg.CDNModelsBaseURL), "/")
	if strings.TrimSpace(cfg.GatewayURL) != "" {
		normURL, err := NormalizeGatewayURL(cfg.GatewayURL)
		if err != nil {
			return err
		}
		cfg.GatewayURL = normURL
	}
	if rawBoot := splitCSV(bootstrapRaw); len(rawBoot) > 0 {
		normBoot, err := nodecore.NormalizeBootstrapDiscoveryURLList(rawBoot, cfg.InsecureWS)
		if err != nil {
			return err
		}
		cfg.BootstrapURLs = normBoot
		cfg.BootstrapURLsExplicit = true
	}
	if cfg.Headless {
		cfg.HasDesktopDisplay = false
	}
	cfg.Devices = splitCSV(devsRaw)
	rawBackends := splitCSV(backendsRaw)
	if len(rawBackends) == 0 {
		rawBackends = cfg.EnabledBackends
	}
	normalizedBackends, err := parseAndNormalizeBackends(rawBackends)
	if err != nil {
		return err
	}
	cfg.EnabledBackends = normalizedBackends
	return validateWorkerModeAndSchedule(cfg, modelsRaw)
}

func validateWorkerNumericBounds(cfg *WorkerConfig, idleFlagExplicit bool) error {
	if idleFlagExplicit || cfg.IdleThresholdSec != 0 {
		if cfg.IdleThresholdSec < minWorkerIdleThresholdSec || cfg.IdleThresholdSec > maxWorkerIdleThresholdSec {
			return derr.Validationf(derr.CodeValidation, "flag --idle-sec=%d must be between %d and %d", cfg.IdleThresholdSec, minWorkerIdleThresholdSec, maxWorkerIdleThresholdSec)
		}
	}
	return validateSoloBootTimeout(cfg.SoloEngineBootTimeout)
}

func validateSoloBootTimeout(timeout time.Duration) error {
	sec := int(timeout / time.Second)
	if sec < minSoloEngineBootTimeoutSec || sec > maxSoloEngineBootTimeoutSec {
		return derr.Validationf(derr.CodeValidation, "flag --solo-boot-timeout-sec=%d must be between %d and %d", sec, minSoloEngineBootTimeoutSec, maxSoloEngineBootTimeoutSec)
	}
	return nil
}

func validateWorkerModeAndSchedule(cfg *WorkerConfig, modelsRaw string) error {
	if cfg.ModelMode != "" || modelsRaw != "" {
		normMode, normModels, err := nodecore.ParseModelSelection(cfg.ModelMode, splitCSV(modelsRaw))
		if err != nil {
			return err
		}
		cfg.ModelMode = string(normMode)
		cfg.SelectedModels = normModels
	}
	if cfg.ScheduleMode != "" {
		schedMode, err := nodecore.ParseIdleMode(cfg.ScheduleMode)
		if err != nil {
			return err
		}
		cfg.ScheduleMode = string(schedMode)
	}
	return nil
}

func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func HasGraphicalDisplayFor(goos string, lookup func(string) (string, bool), args []string) bool {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if hasHeadlessCLIArg(args) || isNonInteractiveRemoteSession(lookup) {
		return false
	}
	switch goos {
	case "windows":
		sess, _ := lookup("SESSIONNAME")
		return !strings.EqualFold(strings.TrimSpace(sess), "Services")
	case "darwin":
		return true
	default:
		disp, _ := lookup("DISPLAY")
		wayland, _ := lookup("WAYLAND_DISPLAY")
		return strings.TrimSpace(disp) != "" || strings.TrimSpace(wayland) != ""
	}
}

func hasHeadlessCLIArg(args []string) bool {
	for _, arg := range args {
		clean := strings.ToLower(strings.TrimSpace(arg))
		if clean == "--headless" || clean == "-headless" || clean == "--headless=true" || clean == "-headless=true" {
			return true
		}
	}
	return false
}

func isNonInteractiveRemoteSession(lookup func(string) (string, bool)) bool {
	for _, key := range []string{"SSH_CONNECTION", "SSH_TTY", "SSH_CLIENT"} {
		if v, _ := lookup(key); strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

func detectSystemLanguage(spec workerEnvSpec) string {
	for _, candidate := range []string{spec.GridLang, spec.LCAll, spec.LCMessages, spec.PosixLang} {
		raw := strings.TrimSpace(candidate)
		if !isNeutralPOSIXLocale(raw) {
			return ClassifyLocaleTag(raw)
		}
	}
	if osLoc := strings.TrimSpace(detectOSLocaleName()); osLoc != "" {
		return ClassifyLocaleTag(osLoc)
	}
	return "en"
}

func ClassifyLocaleTag(raw string) string {
	clean := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(clean, "pt") {
		return "pt-BR"
	}
	return "en"
}

func isNeutralPOSIXLocale(raw string) bool {
	clean := strings.ToLower(strings.TrimSpace(raw))
	return clean == "" || clean == "c" || strings.HasPrefix(clean, "c.") || clean == "posix"
}

func RuntimePathEnv(runtimeDir string) []string {
	return BuildSubprocessEnv(nil, runtimeDir, "")
}

func BuildSubprocessEnv(baseEnv []string, runtimeDir, extraLibPath string) []string {
	raw := baseEnv
	if len(raw) == 0 {
		raw = os.Environ()
	}
	sanitized := sanitizeSubprocessEnviron(raw)
	if runtime.GOOS == "windows" {
		prefix := prependEnvList(runtimeDir, extraLibPath, ";")
		return upsertEnvVar(sanitized, "PATH", prependEnvList(prefix, lookupEnvSlice(sanitized, "PATH"), ";"))
	}
	prefix := prependEnvList(runtimeDir, extraLibPath, ":")
	return upsertEnvVar(sanitized, "LD_LIBRARY_PATH", prependEnvList(prefix, lookupEnvSlice(sanitized, "LD_LIBRARY_PATH"), ":"))
}

func lookupEnvSlice(envSlice []string, key string) string {
	for _, cur := range envSlice {
		curKey, curVal, ok := strings.Cut(cur, "=")
		if ok && strings.EqualFold(curKey, key) {
			return curVal
		}
	}
	return ""
}

func sanitizeSubprocessEnviron(rawEnv []string) []string {
	out := make([]string, 0, len(rawEnv))
	for _, entry := range rawEnv {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || isSensitiveSubprocessEnvKey(key) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func isSensitiveSubprocessEnvKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	for _, suffix := range []string{"_SECRET", "_SECRET_KEY", "_PASSWORD", "_TOKEN", "_TOKENS", "_API_KEY"} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	for _, prefix := range []string{"MODEL_HUB_", "MODELSCOPE_", "CIVITAI_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	switch upper {
	case "GRID_WORKER_TOKEN", "GPU_GRID_TOKEN", "HF_TOKEN", "HF_HUB_TOKEN", "HUGGING_FACE_HUB_TOKEN", "OLLAMA_API_KEY":
		return true
	default:
		return false
	}
}

func prependEnvList(first, existing, sep string) string {
	cleanFirst := strings.TrimSpace(first)
	cleanExisting := strings.TrimSpace(existing)
	if cleanFirst == "" {
		return cleanExisting
	}
	if cleanExisting == "" {
		return cleanFirst
	}
	return cleanFirst + sep + cleanExisting
}

func upsertEnvVar(envSlice []string, key, value string) []string {
	entry := key + "=" + value
	for i, cur := range envSlice {
		curKey, _, ok := strings.Cut(cur, "=")
		if ok && strings.EqualFold(curKey, key) {
			envSlice[i] = entry
			return envSlice
		}
	}
	return append(envSlice, entry)
}
