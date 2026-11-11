package cas_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/cas"
)

func TestZipSlipTraversalBlocked(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "evil.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("../../escaped.txt")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := w.Write([]byte("pwned")); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	if err := cas.ExtractZipSafeWithLimits(zipPath, filepath.Join(dir, "out"), cas.DefaultArchiveLimits()); err == nil {
		t.Fatal("expected ZipSlip traversal entry to be rejected")
	}
}

func TestDownloadVerifiedSHA256TamperDetection(t *testing.T) {
	payload := []byte("deterministic-vulkan-runtime-bytes")
	sum := sha256.Sum256(payload)
	goodHex := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, wErr := w.Write(payload); wErr != nil {
			t.Errorf("write payload: %v", wErr)
		}
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	adapter := cas.NewWorkerCASAdapter(cas.NewRuntimeArchiveVerifier(), srv.Client())
	badDest := filepath.Join(dir, "bad.bin")
	if _, err := adapter.DownloadVerified(context.Background(), srv.URL, badDest, strings.Repeat("0", 64)); err == nil {
		t.Fatal("expected tampered SHA-256 digest to fail verification")
	}

	emptyDigestDest := filepath.Join(dir, "empty-sha.bin")
	if _, err := adapter.DownloadVerified(context.Background(), srv.URL, emptyDigestDest, ""); err == nil {
		t.Fatal("expected empty SHA-256 digest to fail closed")
	}

	goodDest := filepath.Join(dir, "good.bin")
	got, err := adapter.DownloadVerified(context.Background(), srv.URL, goodDest, goodHex)
	if err != nil || got != goodHex {
		t.Fatalf("expected verified download, got=%s err=%v", got, err)
	}
}

func TestEnsureModelCachedStoresByDigestAndFastPathsVerifiedHit(t *testing.T) {
	payload := []byte("gguf-verified-model-tensor-payload-v1")
	sum := sha256.Sum256(payload)
	wantHex := hex.EncodeToString(sum[:])

	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		if _, wErr := w.Write(payload); wErr != nil {
			t.Errorf("write model payload: %v", wErr)
		}
	}))
	t.Cleanup(srv.Close)

	dataDir := t.TempDir()
	verifier := cas.NewRuntimeArchiveVerifier()

	cachedPath, gotHex, err := verifier.EnsureModelCached(context.Background(), srv.Client(), srv.URL+"/models/qwen.gguf", dataDir, wantHex, "qwen2.5-3b.gguf")
	if err != nil {
		t.Fatalf("EnsureModelCached first pull failed: %v", err)
	}
	expectedPath := filepath.Join(dataDir, "models", wantHex, "qwen2.5-3b.gguf")
	if cachedPath != expectedPath || gotHex != wantHex {
		t.Fatalf("unexpected cache location or digest: path=%s want=%s digest=%s", cachedPath, expectedPath, gotHex)
	}
	if downloads.Load() != 1 {
		t.Fatalf("expected 1 HTTP download, got %d", downloads.Load())
	}

	cachedAgain, gotHex2, err := verifier.EnsureModelCached(context.Background(), srv.Client(), srv.URL+"/models/qwen.gguf", dataDir, wantHex, "qwen2.5-3b.gguf")
	if err != nil || cachedAgain != expectedPath || gotHex2 != wantHex {
		t.Fatalf("EnsureModelCached cache-hit failed: path=%s hex=%s err=%v", cachedAgain, gotHex2, err)
	}
	if downloads.Load() != 1 {
		t.Fatalf("expected zero network requests on verified cache hit, got %d", downloads.Load())
	}

	if err := os.WriteFile(expectedPath, []byte("corrupted-local-bytes"), 0o600); err != nil {
		t.Fatalf("corrupt cached file: %v", err)
	}
	repairedPath, gotHex3, err := verifier.EnsureModelCached(context.Background(), srv.Client(), srv.URL+"/models/qwen.gguf", dataDir, wantHex, "qwen2.5-3b.gguf")
	if err != nil || repairedPath != expectedPath || gotHex3 != wantHex || downloads.Load() != 2 {
		t.Fatalf("expected corrupted cache entry to re-download and verify, downloads=%d hex=%s err=%v", downloads.Load(), gotHex3, err)
	}
}

func TestEnsureModelCachedRejectsTraversalFileNames(t *testing.T) {
	dataDir := t.TempDir()
	validHex := strings.Repeat("a", 64)
	adapter := cas.NewWorkerCASAdapter(cas.NewRuntimeArchiveVerifier(), nil)
	badNames := []string{
		"../escape.gguf",
		"..\\escape.gguf",
		"nested/model.gguf",
		"nested\\model.gguf",
		"..",
		".",
	}
	for _, bad := range badNames {
		destPath, gotDigest, err := adapter.EnsureModelCached(context.Background(), dataDir, "test-model", bad, "http://127.0.0.1:9/m.gguf", validHex, nil)
		appErr, ok := derr.AsAppError(err)
		if !ok || appErr.Code != derr.CodePathTraversal || destPath != "" || gotDigest != "" {
			t.Fatalf("expected CodePathTraversal for %q, got path=%q digest=%q err=%v", bad, destPath, gotDigest, err)
		}
	}
}

func TestEnsureModelCachedResumesInterruptedRangeDownload(t *testing.T) {
	fullPayload := []byte("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	sum := sha256.Sum256(fullPayload)
	wantHex := hex.EncodeToString(sum[:])

	dataDir := t.TempDir()
	targetDir := filepath.Join(dataDir, "models", wantHex)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir targetDir: %v", err)
	}
	partPath := filepath.Join(targetDir, "resumable.gguf.part")
	splitAt := 20
	if err := os.WriteFile(partPath, fullPayload[:splitAt], 0o600); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}

	var sawRange atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		if strings.HasPrefix(rng, "bytes=") {
			sawRange.Store(true)
			offStr := strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-")
			off, err := strconv.Atoi(offStr)
			if err == nil && off == splitAt {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, len(fullPayload)-1, len(fullPayload)))
				w.WriteHeader(http.StatusPartialContent)
				if _, wErr := w.Write(fullPayload[off:]); wErr != nil {
					t.Errorf("write range payload: %v", wErr)
				}
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		if _, wErr := w.Write(fullPayload); wErr != nil {
			t.Errorf("write full payload: %v", wErr)
		}
	}))
	t.Cleanup(srv.Close)

	adapter := cas.NewWorkerCASAdapter(cas.NewRuntimeArchiveVerifier(), srv.Client())
	dest, gotHex, err := adapter.EnsureModelCached(context.Background(), dataDir, "resumable", "resumable.gguf", srv.URL+"/resumable.gguf", wantHex, nil)
	if err != nil {
		t.Fatalf("EnsureModelCached resume failed: %v", err)
	}
	if !sawRange.Load() || gotHex != wantHex {
		t.Fatalf("expected HTTP Range resume and verified SHA-256, sawRange=%v gotHex=%s", sawRange.Load(), gotHex)
	}
	diskBytes, err := os.ReadFile(dest)
	if err != nil || string(diskBytes) != string(fullPayload) {
		t.Fatalf("unexpected resumed payload on disk: err=%v", err)
	}
}

func TestArchiveBombEntryAndTotalLimitsEnforcedInZipAndTarGz(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bomb.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	for i := range 3 {
		w, cErr := zw.Create(fmt.Sprintf("chunk-%d.bin", i))
		if cErr != nil {
			t.Fatal(cErr)
		}
		if _, wErr := w.Write(bytes.Repeat([]byte("A"), 512)); wErr != nil {
			t.Fatal(wErr)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}

	if err := cas.ExtractZipSafeWithLimits(zipPath, filepath.Join(dir, "zip-entry-cap"), cas.ArchiveLimits{
		MaxEntryBytes: 256,
		MaxTotalBytes: 4096,
	}); err == nil {
		t.Fatal("expected zip entry exceeding MaxEntryBytes to be rejected")
	}
	if err := cas.ExtractZipSafeWithLimits(zipPath, filepath.Join(dir, "zip-total-cap"), cas.ArchiveLimits{
		MaxEntryBytes: 1024,
		MaxTotalBytes: 1024,
	}); err == nil {
		t.Fatal("expected zip archive exceeding MaxTotalBytes budget to be rejected")
	}

	tgzPath := writeSyntheticTarGz(t, dir, "bomb.tar.gz", []tarTestEntry{
		{name: "part1.bin", typeflag: tar.TypeReg, data: bytes.Repeat([]byte("B"), 600)},
		{name: "part2.bin", typeflag: tar.TypeReg, data: bytes.Repeat([]byte("C"), 600)},
	})
	if err := cas.ExtractTarGzSafeWithLimits(tgzPath, filepath.Join(dir, "tgz-total-cap"), cas.ArchiveLimits{
		MaxEntryBytes: 800,
		MaxTotalBytes: 1000,
	}); err == nil {
		t.Fatal("expected tar.gz archive exceeding MaxTotalBytes budget to be rejected")
	}
}

func TestExtractTarGzSafeBlocksEscapingSymlinksAndAllowsConfinedRelativeSymlink(t *testing.T) {
	dir := t.TempDir()
	escapeRel := writeSyntheticTarGz(t, dir, "escape-rel.tar.gz", []tarTestEntry{
		{name: "sub/escape-link", typeflag: tar.TypeSymlink, linkname: "../../outside.txt"},
	})
	if err := cas.ExtractTarGzSafeWithLimits(escapeRel, filepath.Join(dir, "out-rel"), cas.DefaultArchiveLimits()); err == nil {
		t.Fatal("expected relative escaping tar symlink to be rejected")
	}

	escapeAbs := writeSyntheticTarGz(t, dir, "escape-abs.tar.gz", []tarTestEntry{
		{name: "bin/abs-link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
	})
	if err := cas.ExtractTarGzSafeWithLimits(escapeAbs, filepath.Join(dir, "out-abs"), cas.DefaultArchiveLimits()); err == nil {
		t.Fatal("expected absolute tar symlink target to be rejected")
	}

	validInternal := writeSyntheticTarGz(t, dir, "valid-link.tar.gz", []tarTestEntry{
		{name: "lib/libggml.so.0", typeflag: tar.TypeReg, data: []byte("ELF-STUB")},
		{name: "bin/libggml.so", typeflag: tar.TypeSymlink, linkname: "../lib/libggml.so.0"},
	})
	err := cas.ExtractTarGzSafeWithLimits(validInternal, filepath.Join(dir, "out-valid"), cas.DefaultArchiveLimits())
	if appErr, ok := derr.AsAppError(err); ok && appErr.Code == derr.CodePathTraversal {
		t.Fatalf("confined internal relative symlink must not fail with CodePathTraversal: %v", err)
	}
}

type tarTestEntry struct {
	name     string
	typeflag byte
	linkname string
	data     []byte
}

func writeSyntheticTarGz(t *testing.T, dir, fileName string, entries []tarTestEntry) string {
	t.Helper()
	full := filepath.Join(dir, fileName)
	f, err := os.Create(full)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Linkname: e.linkname,
			Mode:     0o755,
			Size:     int64(len(e.data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if len(e.data) > 0 {
			if _, err := tw.Write(e.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestAvailableDiskSpaceAndListCachedDigestsInventory(t *testing.T) {
	dataDir := t.TempDir()
	nonExistentChild := filepath.Join(dataDir, "nested", "models", "future")

	freeBytes, err := cas.AvailableDiskBytes(nonExistentChild)
	if err != nil || freeBytes == 0 {
		t.Fatalf("expected positive free disk bytes on ancestor of non-existent dir, bytes=%d err=%v", freeBytes, err)
	}
	freeMiB, err := cas.AvailableDiskMiB(nonExistentChild)
	if err != nil || freeMiB <= 0 {
		t.Fatalf("expected positive free disk MiB, mib=%d err=%v", freeMiB, err)
	}

	validA := strings.Repeat("a", 64)
	validB := strings.Repeat("f", 64)
	partOnlyDigest := strings.Repeat("e", 64)

	if err := os.MkdirAll(filepath.Join(dataDir, "models", strings.ToUpper(validB)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "models", strings.ToUpper(validB), "model-b.gguf"), []byte("weights-b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "models", validA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "models", validA, "model-a.gguf"), []byte("weights-a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "models", partOnlyDigest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "models", partOnlyDigest, "incomplete.gguf.part"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "models", "not-a-sha256"), 0o755); err != nil {
		t.Fatal(err)
	}

	adapter := cas.NewWorkerCASAdapter(cas.NewRuntimeArchiveVerifier(), nil)
	digests, err := adapter.ListCachedDigests(dataDir)
	if err != nil {
		t.Fatalf("ListCachedDigests failed: %v", err)
	}
	if len(digests) != 2 || digests[0] != validA || digests[1] != validB {
		t.Fatalf("expected sorted completed SHA-256 digests [%s %s], got %+v", validA, validB, digests)
	}
	adapterMiB, err := adapter.AvailableDiskMiB(dataDir)
	if err != nil || adapterMiB <= 0 {
		t.Fatalf("expected WorkerCASAdapter.AvailableDiskMiB > 0, got %d err=%v", adapterMiB, err)
	}
}

func TestEnsureModelCachedPreservesPartFileOnMidStreamDropAndResumes(t *testing.T) {
	fullPayload := []byte("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-resumable-tail")
	sum := sha256.Sum256(fullPayload)
	wantHex := hex.EncodeToString(sum[:])
	splitAt := 28

	var attempts atomic.Int32
	var sawResumeRange atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(fullPayload)))
			w.WriteHeader(http.StatusOK)
			if _, wErr := w.Write(fullPayload[:splitAt]); wErr != nil {
				t.Logf("write partial payload before hijack: %v", wErr)
			}
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			if hj, ok := w.(http.Hijacker); ok {
				if conn, rw, hErr := hj.Hijack(); hErr == nil && rw != nil {
					if cErr := conn.Close(); cErr != nil {
						t.Logf("hijacked conn close: %v", cErr)
					}
				}
			}
			return
		}
		rng := r.Header.Get("Range")
		if rng == fmt.Sprintf("bytes=%d-", splitAt) {
			sawResumeRange.Store(true)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", splitAt, len(fullPayload)-1, len(fullPayload)))
			w.WriteHeader(http.StatusPartialContent)
			if _, wErr := w.Write(fullPayload[splitAt:]); wErr != nil {
				t.Errorf("write resumed payload: %v", wErr)
			}
			return
		}
		w.WriteHeader(http.StatusOK)
		if _, wErr := w.Write(fullPayload); wErr != nil {
			t.Errorf("write fallback payload: %v", wErr)
		}
	}))
	t.Cleanup(srv.Close)

	dataDir := t.TempDir()
	adapter := cas.NewWorkerCASAdapter(cas.NewRuntimeArchiveVerifier(), srv.Client())
	firstPath, firstHex, firstErr := adapter.EnsureModelCached(context.Background(), dataDir, "midstream", "midstream.gguf", srv.URL+"/midstream.gguf", wantHex, nil)
	if firstErr == nil {
		t.Fatalf("expected mid-stream TCP drop on attempt #1 to return an error, got path=%s hex=%s", firstPath, firstHex)
	}

	partPath := filepath.Join(dataDir, "models", wantHex, "midstream.gguf.part")
	st, statErr := os.Stat(partPath)
	if statErr != nil || st.Size() != int64(splitAt) {
		t.Fatalf("expected .part file preserved with %d bytes after mid-stream drop, stat=%+v err=%v", splitAt, st, statErr)
	}

	dest, gotHex, secondErr := adapter.EnsureModelCached(context.Background(), dataDir, "midstream", "midstream.gguf", srv.URL+"/midstream.gguf", wantHex, nil)
	if secondErr != nil || gotHex != wantHex || !sawResumeRange.Load() {
		t.Fatalf("expected Range resume on attempt #2 to succeed, sawRange=%v gotHex=%s err=%v", sawResumeRange.Load(), gotHex, secondErr)
	}
	if _, err := os.Stat(partPath); !os.IsNotExist(err) {
		t.Fatalf("expected .part file renamed/cleaned after completed resume, stat err=%v", err)
	}
	finalBytes, readErr := os.ReadFile(dest)
	if readErr != nil || !bytes.Equal(finalBytes, fullPayload) {
		t.Fatalf("expected exact resumed payload on disk, readErr=%v", readErr)
	}
}
