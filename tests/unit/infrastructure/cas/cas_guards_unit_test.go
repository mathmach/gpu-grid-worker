package cas_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/cas"
)

func hasCode(err error, code derr.Code) bool {
	appErr, ok := derr.AsAppError(err)
	return ok && appErr.Code == code
}

func buildInMemoryZip(t *testing.T, entries []struct {
	name    string
	mode    fs.FileMode
	payload string
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, ent := range entries {
		hdr := &zip.FileHeader{Name: ent.name, Method: zip.Deflate}
		if ent.mode != 0 {
			hdr.SetMode(ent.mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("zip CreateHeader(%q): %v", ent.name, err)
		}
		if _, err := io.WriteString(w, ent.payload); err != nil {
			t.Fatalf("zip WriteString(%q): %v", ent.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip Close: %v", err)
	}
	return buf.Bytes()
}

func buildInMemoryTarGz(t *testing.T, headers []*tar.Header, bodies []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for idx, hdr := range headers {
		body := bodies[idx]
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar WriteHeader(%q): %v", hdr.Name, err)
		}
		if hdr.Typeflag == tar.TypeReg && len(body) > 0 {
			if _, err := io.WriteString(tw, body); err != nil {
				t.Fatalf("tar WriteString(%q): %v", hdr.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

func validateInMemoryZipStream(raw []byte, virtualDest string, limits cas.ArchiveLimits) error {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return err
	}
	var total uint64
	for _, zf := range zr.File {
		if _, err := cas.SafeJoin(virtualDest, zf.Name); err != nil {
			return err
		}
		if zf.Mode()&fs.ModeSymlink != 0 {
			return derr.New(derr.CodePathTraversal, 400, "zip symlink rejected", nil)
		}
		total += zf.UncompressedSize64
		if zf.UncompressedSize64 > uint64(limits.MaxEntryBytes) || total > uint64(limits.MaxTotalBytes) {
			return derr.New(derr.CodeValidation, 413, "zip limit exceeded", nil)
		}
	}
	return nil
}

func validateInMemoryTarGzStream(raw []byte, virtualDest string, limits cas.ArchiveLimits) (retErr error) {
	gzr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer func() {
		if cErr := gzr.Close(); cErr != nil && retErr == nil {
			retErr = cErr
		}
	}()
	tr := tar.NewReader(gzr)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if _, jErr := cas.SafeJoin(virtualDest, hdr.Name); jErr != nil {
			return jErr
		}
		switch hdr.Typeflag {
		case tar.TypeLink:
			return derr.New(derr.CodePathTraversal, 400, "tar hardlink rejected", nil)
		case tar.TypeSymlink:
			if strings.HasPrefix(hdr.Linkname, "/") {
				return derr.New(derr.CodePathTraversal, 400, "absolute tar symlink rejected", nil)
			}
			relParent := filepath.Dir(filepath.FromSlash(hdr.Name))
			joinedLink := filepath.ToSlash(filepath.Join(relParent, filepath.FromSlash(hdr.Linkname)))
			if _, sErr := cas.SafeJoin(virtualDest, joinedLink); sErr != nil {
				return sErr
			}
		case tar.TypeReg:
			total += hdr.Size
			if hdr.Size > limits.MaxEntryBytes || total > limits.MaxTotalBytes {
				return derr.New(derr.CodeValidation, 413, "tar limit exceeded", nil)
			}
		}
	}
}

func TestSafeJoinAndInMemoryZipTarArchiveTraversalAndSymlinkGuards(t *testing.T) {
	limits := cas.DefaultArchiveLimits()
	if limits.MaxEntryBytes != cas.DefaultMaxArchiveEntryBytes || limits.MaxTotalBytes != cas.DefaultMaxArchiveTotalBytes {
		t.Fatalf("unexpected DefaultArchiveLimits: %+v", limits)
	}

	virtualRoot := filepath.Join("virtual-sandbox", "runtime-root")
	joined, err := cas.SafeJoin(virtualRoot, "bin/llama-server")
	if err != nil || !strings.HasSuffix(filepath.ToSlash(joined), "runtime-root/bin/llama-server") {
		t.Fatalf("expected clean SafeJoin inside root, got %q err=%v", joined, err)
	}

	for _, badEntry := range []string{
		"../escape.dll",
		"bin/../../../etc/passwd",
		"/etc/shadow",
		`\windows\system32\cmd.exe`,
		"file\x00null.so",
	} {
		if _, err := cas.SafeJoin(virtualRoot, badEntry); !hasCode(err, derr.CodePathTraversal) {
			t.Fatalf("expected CodePathTraversal for %q, got %v", badEntry, err)
		}
	}

	zipTraversal := buildInMemoryZip(t, []struct {
		name    string
		mode    fs.FileMode
		payload string
	}{
		{name: "../../outside.exe", mode: 0o644, payload: "mz"},
	})
	if err := validateInMemoryZipStream(zipTraversal, virtualRoot, limits); !hasCode(err, derr.CodePathTraversal) {
		t.Fatalf("expected zip traversal rejection, got %v", err)
	}

	zipSymlink := buildInMemoryZip(t, []struct {
		name    string
		mode    fs.FileMode
		payload string
	}{
		{name: "bin/link-out", mode: fs.ModeSymlink | 0o777, payload: "../../etc/passwd"},
	})
	if err := validateInMemoryZipStream(zipSymlink, virtualRoot, limits); !hasCode(err, derr.CodePathTraversal) {
		t.Fatalf("expected zip symlink rejection, got %v", err)
	}

	zipBomb := buildInMemoryZip(t, []struct {
		name    string
		mode    fs.FileMode
		payload string
	}{
		{name: "bin/oversized.bin", mode: 0o644, payload: strings.Repeat("Z", 128)},
	})
	if err := validateInMemoryZipStream(zipBomb, virtualRoot, cas.ArchiveLimits{MaxEntryBytes: 32, MaxTotalBytes: 64}); !hasCode(err, derr.CodeValidation) {
		t.Fatalf("expected zip entry byte ceiling rejection, got %v", err)
	}

	tarEscapingSymlink := buildInMemoryTarGz(t, []*tar.Header{
		{Name: "lib/escape_sym.so", Typeflag: tar.TypeSymlink, Linkname: "../../../etc/ld.so.preload", Mode: 0o777},
	}, []string{""})
	if err := validateInMemoryTarGzStream(tarEscapingSymlink, virtualRoot, limits); !hasCode(err, derr.CodePathTraversal) {
		t.Fatalf("expected escaping tar symlink rejection, got %v", err)
	}

	tarHardlink := buildInMemoryTarGz(t, []*tar.Header{
		{Name: "lib/hardlink.so", Typeflag: tar.TypeLink, Linkname: "bin/llama-server", Mode: 0o644},
	}, []string{""})
	if err := validateInMemoryTarGzStream(tarHardlink, virtualRoot, limits); !hasCode(err, derr.CodePathTraversal) {
		t.Fatalf("expected tar hardlink rejection, got %v", err)
	}

	tarSafe := buildInMemoryTarGz(t, []*tar.Header{
		{Name: "lib/libggml.so.0.1", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "lib/libggml.so", Typeflag: tar.TypeSymlink, Linkname: "libggml.so.0.1", Mode: 0o777},
	}, []string{"ELF-BINARY", ""})
	if err := validateInMemoryTarGzStream(tarSafe, virtualRoot, limits); err != nil {
		t.Fatalf("expected internal relative tar symlink to pass validation, got %v", err)
	}
}

func TestDownloadVerifiedAndEnsureModelCachedInputAndSSRFGuards(t *testing.T) {
	validSHA := strings.Repeat("b", 64)
	ctx := context.Background()
	adapter := cas.NewWorkerCASAdapter(cas.NewRuntimeArchiveVerifier(), nil)

	if _, err := adapter.DownloadVerified(ctx, "https://cdn.example.com/model.gguf", "virtual/model.gguf", "bad-sha"); !hasCode(err, derr.CodeDigestMismatch) {
		t.Fatalf("expected CodeDigestMismatch on invalid SHA-256 hex, got %v", err)
	}

	for _, blockedURL := range []string{
		"",
		"ftp://cdn.example.com/model.gguf",
		"https://admin:secret@cdn.example.com/model.gguf",
		"http://0.0.0.0:8080/model.gguf",
		"http://[::]:8080/model.gguf",
		"http://169.254.169.254/latest/meta-data/",
		"http://[fe80::1]/metadata",
		"http://100.64.12.34/cgnat-internal",
		"http://224.0.0.1/multicast",
	} {
		if _, err := adapter.DownloadVerified(ctx, blockedURL, "virtual/model.gguf", validSHA); !hasCode(err, derr.CodeValidation) {
			t.Fatalf("expected SSRF/URL CodeValidation for %q, got %v", blockedURL, err)
		}
	}

	if _, _, err := adapter.EnsureModelCached(ctx, "", "qwen-7b", "qwen.gguf", "https://cdn.example.com/qwen.gguf", validSHA, nil); !hasCode(err, derr.CodeValidation) {
		t.Fatalf("expected empty cacheRoot rejection, got %v", err)
	}
	for _, badFile := range []string{"../qwen.gguf", "sub/qwen.gguf", `sub\qwen.gguf`, "C:qwen.gguf", "qwen\x00.gguf"} {
		if _, _, err := adapter.EnsureModelCached(ctx, "virtual-cache", "qwen-7b", badFile, "https://cdn.example.com/qwen.gguf", validSHA, nil); !hasCode(err, derr.CodePathTraversal) {
			t.Fatalf("expected CodePathTraversal for model fileName %q, got %v", badFile, err)
		}
	}

	gw := cas.NewGatewayClient()
	if _, err := gw.BuildWorkerWSURL("http://public.grid.example.com", false); !hasCode(err, derr.CodeValidation) {
		t.Fatalf("expected public WAN plain HTTP gateway to reject ws:// when allowInsecureWS=false, got %v", err)
	}
	wssURL, err := gw.BuildWorkerWSURL("https://public.grid.example.com/", false)
	if err != nil || wssURL != "wss://public.grid.example.com/ws/worker" {
		t.Fatalf("expected wss:// worker URL, got %q err=%v", wssURL, err)
	}
	lanWS, err := gw.BuildWorkerWSURL("http://192.168.10.25:8000", false)
	if err != nil || lanWS != "ws://192.168.10.25:8000/ws/worker" {
		t.Fatalf("expected LAN ws:// worker URL, got %q err=%v", lanWS, err)
	}

	for _, badBrowserURL := range []string{
		"http://:8080/portal",
		"https://:443/checkout",
		"http:///missing-host",
		"javascript:alert(1)",
		"https://user:pass@example.com/portal",
	} {
		if _, err := cas.ValidateExternalBrowserURL(badBrowserURL); !hasCode(err, derr.CodeValidation) {
			t.Fatalf("expected ValidateExternalBrowserURL(%q) to fail with CodeValidation, got %v", badBrowserURL, err)
		}
	}
	cleanPortal, err := cas.ValidateExternalBrowserURL("https://portal.example.com/provider/payouts")
	if err != nil || cleanPortal != "https://portal.example.com/provider/payouts" {
		t.Fatalf("expected valid external browser URL to pass, got %q err=%v", cleanPortal, err)
	}
}
