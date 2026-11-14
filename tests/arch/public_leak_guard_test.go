package arch_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPublicWorkerRepoZeroProprietaryLeak(t *testing.T) {
	root := findRepoRoot(t)
	selfFile := filepath.Clean(filepath.Join(root, "tests", "arch", "public_leak_guard_test.go"))

	requiredOSSFiles := []string{
		"LICENSE",
		"NOTICE",
		"README.md",
		"SECURITY.md",
		"CONTRIBUTING.md",
		"build.bat",
		"validate.bat",
		filepath.Join(".github", "workflows", "ci-release.yml"),
	}
	for _, req := range requiredOSSFiles {
		full := filepath.Join(root, req)
		if st, err := os.Stat(full); err != nil || st.IsDir() || st.Size() == 0 {
			t.Errorf("required OSS governance/build file missing or empty: %s", req)
		}
	}

	forbiddenCaseInsensitive := []string{
		"gpu-grid/internal/",
		"gpu-grid/cmd/",
		"stripe",
		"whsec_",
		"sqlite",
		"jackc/pgx",
		"pgdialect",
		"persistence/postgres",
		"database_url",
		"db_schema",
		"uptrace/bun",
		"pressly/goose",
		"tiktoken",
		"caarlos0/env",
		"godotenv",
		"escrow_lot",
		"credit_hold",
		"promo_credit",
		"pending_settlement",
		"waitlist_lead",
		"pending_registration",
		"domain/orderbook",
		"system_admin",
		"grid_admin_session",
		"brl_per_credit_cents",
		"base_compute_rate_per_1m",
		"allow_dev_simulator",
		"smtp_",
		"admin_bootstrap",
	}

	for _, single := range []string{"go.mod", "README.md", "SECURITY.md", "CONTRIBUTING.md", "NOTICE", "build.bat", "validate.bat"} {
		full := filepath.Join(root, single)
		if _, err := os.Stat(full); err == nil {
			assertZeroProprietaryTokens(t, full, forbiddenCaseInsensitive)
		}
	}

	for _, tree := range []string{"cmd", "pkg", "tests", ".github"} {
		base := filepath.Join(root, tree)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		walkErr := filepath.WalkDir(base, func(path string, d os.DirEntry, wErr error) error {
			if wErr != nil || d.IsDir() || filepath.Clean(path) == selfFile {
				return wErr
			}
			assertZeroProprietaryTokens(t, path, forbiddenCaseInsensitive)
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", tree, walkErr)
		}
	}
}

func assertZeroProprietaryTokens(t *testing.T, path string, forbidden []string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lower := strings.ToLower(string(raw))
	for _, tok := range forbidden {
		if strings.Contains(lower, tok) {
			t.Errorf("proprietary or gateway-only token %q leaked into public worker repo file %s", tok, path)
		}
	}
}

func TestWorkerGitIgnoreAndZeroTrackedBuildArtifacts(t *testing.T) {
	root := findRepoRoot(t)
	assertWorkerGitIgnoreContainsRequiredLines(t, filepath.Join(root, ".gitignore"), []string{
		"/dist/",
		"/build/",
		"/out/",
		"/coverage/",
		"node_modules/",
		".next/",
		"_next/",
	})
	assertWorkerZeroTrackedGeneratedArtifacts(t, root)
}

func assertWorkerGitIgnoreContainsRequiredLines(t *testing.T, gitignorePath string, required []string) {
	t.Helper()
	raw, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read %s: %v", gitignorePath, err)
	}
	present := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		present[strings.TrimSpace(strings.TrimRight(line, "\r"))] = true
	}
	for _, req := range required {
		if !present[req] {
			t.Errorf("%s is missing required ignore rule %q", gitignorePath, req)
		}
	}
}

func assertWorkerZeroTrackedGeneratedArtifacts(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return
	}
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	out, err := exec.Command("git", "-C", root, "ls-files").Output()
	if err != nil {
		t.Fatalf("git ls-files %s: %v", root, err)
	}
	for _, rawLine := range strings.Split(string(out), "\n") {
		rel := strings.TrimSpace(strings.TrimRight(rawLine, "\r"))
		if rel != "" && isForbiddenWorkerTrackedArtifact(filepath.ToSlash(rel)) {
			t.Errorf("generated build artifact %q must not be tracked in git", rel)
		}
	}
}

func isForbiddenWorkerTrackedArtifact(slash string) bool {
	for _, prefix := range []string{"dist/", "build/", "out/", "coverage/", "assets/", "data/"} {
		if strings.HasPrefix(slash, prefix) {
			return true
		}
	}
	for _, part := range strings.Split(slash, "/") {
		if slices.Contains([]string{".next", "_next", "node_modules"}, part) {
			return true
		}
	}
	for _, suffix := range []string{".exe", ".dll", ".so", ".dylib", ".test", ".tsbuildinfo"} {
		if strings.HasSuffix(slash, suffix) {
			return true
		}
	}
	return false
}
