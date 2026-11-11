package workerconfig_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/workerconfig"
)

func TestClassifyLocaleTag(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "pt-BR", want: "pt-BR"},
		{raw: "pt_BR.UTF-8", want: "pt-BR"},
		{raw: "pt-PT", want: "pt-BR"},
		{raw: "en-US", want: "en"},
		{raw: "en_GB.UTF-8", want: "en"},
		{raw: "es-ES", want: "en"},
		{raw: "", want: "en"},
	}
	for _, tc := range cases {
		if got := workerconfig.ClassifyLocaleTag(tc.raw); got != tc.want {
			t.Fatalf("ClassifyLocaleTag(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestLoadWorkerConfigHeadlessFlagsAndEnvValidation(t *testing.T) {
	t.Setenv("GPU_GRID_LANG", "pt_BR.UTF-8")
	t.Setenv("GPU_GRID_BACKENDS", "")
	t.Setenv("ENABLED_BACKENDS", "")
	cfg, err := workerconfig.LoadWorkerConfig([]string{"--headless", "--gateway", "grid.example.com"})
	if err != nil {
		t.Fatalf("LoadWorkerConfig failed: %v", err)
	}
	if !cfg.Headless || cfg.HasDesktopDisplay {
		t.Fatalf("expected --headless to set Headless=true and HasDesktopDisplay=false: %+v", cfg)
	}
	if cfg.DefaultLanguage != "pt-BR" || cfg.GatewayURL != "https://grid.example.com" {
		t.Fatalf("unexpected locale or gateway URL: %+v", cfg)
	}

	t.Setenv("GPU_GRID_LANG", "fr-FR")
	cfgEN, err := workerconfig.LoadWorkerConfig(nil)
	if err != nil {
		t.Fatalf("LoadWorkerConfig default failed: %v", err)
	}
	if cfgEN.DefaultLanguage != "en" || cfgEN.EnabledBackendsExplicit {
		t.Fatalf("expected DefaultLanguage=en and EnabledBackendsExplicit=false, got %+v", cfgEN)
	}

	cfgExplicit, err := workerconfig.LoadWorkerConfig([]string{"--backends", "llama_local,whisper_local", "--idle-sec", "120"})
	if err != nil {
		t.Fatalf("LoadWorkerConfig explicit flags failed: %v", err)
	}
	if !cfgExplicit.EnabledBackendsExplicit || len(cfgExplicit.EnabledBackends) != 2 || cfgExplicit.IdleThresholdSec != 120 {
		t.Fatalf("unexpected explicit worker flags: %+v", cfgExplicit)
	}

	for _, badArgs := range [][]string{
		{"--unknown-flag"},
		{"--idle-sec", "0"},
		{"--idle-sec", "10"},
		{"--idle-sec", "7200"},
	} {
		if _, wErr := workerconfig.LoadWorkerConfig(badArgs); wErr == nil {
			t.Fatalf("expected LoadWorkerConfig(%v) to fail", badArgs)
		}
	}
}

func TestValidateWorkerEnvStrictBounds(t *testing.T) {
	validEnv := map[string]string{
		"GPU_GRID_IDLE_SEC":              "60",
		"GPU_GRID_SOLO_BOOT_TIMEOUT_SEC": "120",
		"GPU_GRID_INSECURE_WS":           "false",
		"GPU_GRID_MODEL_MODE":            "auto",
		"GPU_GRID_SCHEDULE_MODE":         "idle",
		"GPU_GRID_BACKENDS":              "llama_local,whisper_local",
	}
	err := workerconfig.ValidateWorkerEnv(func(k string) (string, bool) {
		v, ok := validEnv[k]
		return v, ok
	})
	if err != nil {
		t.Fatalf("expected valid worker env accepted, got %v", err)
	}

	badCases := []map[string]string{
		{"GPU_GRID_IDLE_SEC": "5"},
		{"GPU_GRID_INSECURE_WS": "maybe"},
		{"GPU_GRID_MODEL_MODE": "invalid"},
		{"GPU_GRID_BACKENDS": "unknown_backend"},
	}
	for _, env := range badCases {
		badErr := workerconfig.ValidateWorkerEnv(func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		})
		if badErr == nil {
			t.Fatalf("expected ValidateWorkerEnv(%v) to fail", env)
		}
	}
}

func TestNormalizeGatewayURLLANHttpVersusPublicWANHttpsAndInvalidURLs(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "localhost:8000", want: "http://localhost:8000"},
		{raw: "127.0.0.1:8000/", want: "http://127.0.0.1:8000"},
		{raw: "[::1]:8000", want: "http://[::1]:8000"},
		{raw: "::1", want: "http://[::1]"},
		{raw: "10.0.0.15:8000", want: "http://10.0.0.15:8000"},
		{raw: "172.16.10.5:8000", want: "http://172.16.10.5:8000"},
		{raw: "192.168.1.50:8000", want: "http://192.168.1.50:8000"},
		{raw: "172.32.0.1:8000", want: "https://172.32.0.1:8000"},
		{raw: "203.0.113.10:8000", want: "https://203.0.113.10:8000"},
		{raw: "grid.example.com", want: "https://grid.example.com"},
		{raw: "grid.example.com:8443/", want: "https://grid.example.com:8443"},
		{raw: "http://grid.example.com:8000/", want: "http://grid.example.com:8000"},
		{raw: "ws://192.168.1.10:8000/", want: "http://192.168.1.10:8000"},
		{raw: "wss://grid.example.com/", want: "https://grid.example.com"},
	}
	for _, tc := range cases {
		got, err := workerconfig.NormalizeGatewayURL(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("NormalizeGatewayURL(%q) = (%q, %v), want (%q, nil)", tc.raw, got, err, tc.want)
		}
	}

	for _, bad := range []string{"", "   ", "ftp://grid.example.com", "http://", "https://", "grid.example.com:99999", "http://bad host:8000", "https://user:pass@grid.example.com"} {
		if got, err := workerconfig.NormalizeGatewayURL(bad); err == nil {
			t.Fatalf("expected NormalizeGatewayURL(%q) to fail, got %q", bad, got)
		}
	}
}

func TestHasGraphicalDisplayAndSubprocessEnvSecretSanitization(t *testing.T) {
	emptyLookup := func(string) (string, bool) { return "", false }
	if !workerconfig.HasGraphicalDisplayFor("windows", emptyLookup, nil) {
		t.Fatal("expected interactive Windows desktop session to report graphical display")
	}
	svcLookup := func(k string) (string, bool) {
		if k == "SESSIONNAME" {
			return "Services", true
		}
		return "", false
	}
	if workerconfig.HasGraphicalDisplayFor("windows", svcLookup, nil) {
		t.Fatal("expected Windows Services session to report HasGraphicalDisplay=false")
	}
	if workerconfig.HasGraphicalDisplayFor("windows", emptyLookup, []string{"--headless"}) {
		t.Fatal("expected --headless CLI flag to report HasGraphicalDisplay=false")
	}

	baseEnv := []string{
		"SAFE_CUSTOM_FLAG=enabled",
		"HF_TOKEN=hf_secret_token_should_never_leak",
		"HF_HUB_TOKEN=hf_hub_secret_should_never_leak",
		"HUGGING_FACE_HUB_TOKEN=hf_legacy_secret_should_never_leak",
		"UPSTREAM_API_SECRET=secret_should_never_leak",
		"MAIL_SERVER_PASSWORD=password_should_never_leak",
		"GRID_WORKER_TOKEN=wk_secret_1",
		"GPU_GRID_TOKEN=wk_secret_2",
		"PATH=/usr/local/bin;/usr/bin",
		"LD_LIBRARY_PATH=/usr/local/cuda/lib64",
	}
	got := workerconfig.BuildSubprocessEnv(baseEnv, "/opt/grid/runtime", "/opt/grid/extra")
	envMap := make(map[string]string, len(got))
	for _, kv := range got {
		if k, v, ok := strings.Cut(kv, "="); ok {
			envMap[strings.ToUpper(k)] = v
		}
	}
	if envMap["SAFE_CUSTOM_FLAG"] != "enabled" {
		t.Fatalf("expected non-sensitive env var preserved, got %v", got)
	}
	for _, forbidden := range []string{"HF_TOKEN", "HF_HUB_TOKEN", "HUGGING_FACE_HUB_TOKEN", "UPSTREAM_API_SECRET", "MAIL_SERVER_PASSWORD", "GRID_WORKER_TOKEN", "GPU_GRID_TOKEN"} {
		if val, exists := envMap[forbidden]; exists {
			t.Fatalf("expected sensitive env key %s stripped from subprocess env, found %q", forbidden, val)
		}
	}
	assertRuntimePathPrepended(t, envMap)
}

func assertRuntimePathPrepended(t *testing.T, envMap map[string]string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if want := "/opt/grid/runtime;/opt/grid/extra;/usr/local/bin;/usr/bin"; envMap["PATH"] != want {
			t.Fatalf("expected Windows PATH prepended as %q, got %q", want, envMap["PATH"])
		}
		return
	}
	if want := "/opt/grid/runtime:/opt/grid/extra:/usr/local/cuda/lib64"; envMap["LD_LIBRARY_PATH"] != want {
		t.Fatalf("expected Linux LD_LIBRARY_PATH prepended as %q, got %q", want, envMap["LD_LIBRARY_PATH"])
	}
}
