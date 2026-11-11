package cas

import (
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

type OSBrowserLauncher struct{}

func NewOSBrowserLauncher() *OSBrowserLauncher {
	return &OSBrowserLauncher{}
}

func ValidateExternalBrowserURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || strings.ContainsAny(trimmed, " \t\r\n\x00") {
		return "", derr.New(derr.CodeValidation, 400, "invalid browser target URL", nil)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return "", derr.New(derr.CodeValidation, 400, "invalid browser target URL", nil)
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", derr.New(derr.CodeValidation, 400, "browser target URL must use http or https scheme", nil)
	}
	return parsed.String(), nil
}

func (l *OSBrowserLauncher) OpenExternalURL(rawURL string) error {
	cleanURL, err := ValidateExternalBrowserURL(rawURL)
	if err != nil {
		return err
	}
	cmd := buildBrowserOpenCommand(runtime.GOOS, cleanURL)
	if err := cmd.Start(); err != nil {
		return derr.Wrap(derr.CodeInternal, 500, "failed to launch system default browser", err)
	}
	go waitBrowserHelperQuietly(cmd)
	return nil
}

func buildBrowserOpenCommand(goos, targetURL string) *exec.Cmd {
	switch goos {
	case "windows":
		return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", targetURL)
	case "darwin":
		return exec.Command("open", targetURL)
	default:
		return exec.Command("xdg-open", targetURL)
	}
}

func waitBrowserHelperQuietly(cmd *exec.Cmd) {
	if cmd != nil {
		if err := cmd.Wait(); err != nil {
			return
		}
	}
}
