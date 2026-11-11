package cas_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/cas"
)

type tarLinkEntry struct {
	name     string
	typeflag byte
	linkname string
	body     string
}

func writeLinkTestTarGz(t *testing.T, archivePath string, entries []tarLinkEntry) {
	t.Helper()
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create tar.gz: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, ent := range entries {
		hdr := &tar.Header{
			Name:     ent.name,
			Typeflag: ent.typeflag,
			Linkname: ent.linkname,
			Mode:     0o644,
			Size:     int64(len(ent.body)),
		}
		if ent.typeflag == tar.TypeDir {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %s: %v", ent.name, err)
		}
		if ent.body != "" {
			if _, err := tw.Write([]byte(ent.body)); err != nil {
				t.Fatalf("write tar body %s: %v", ent.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close tar.gz file: %v", err)
	}
}

func hostAllowsFileSymlinks(t *testing.T) bool {
	t.Helper()
	probeDir := t.TempDir()
	target := filepath.Join(probeDir, "target.txt")
	if err := os.WriteFile(target, []byte("ok"), 0o600); err != nil {
		return false
	}
	return os.Symlink("target.txt", filepath.Join(probeDir, "link.txt")) == nil
}

func TestTarGzSymlinkLexicalCollapseTraversalBlockedAndConfinedSharedLibAllowed(t *testing.T) {
	dir := t.TempDir()

	singleEscapeArchive := filepath.Join(dir, "single-escape.tar.gz")
	writeLinkTestTarGz(t, singleEscapeArchive, []tarLinkEntry{
		{name: "a/b/", typeflag: tar.TypeDir},
		{name: "a/b/escape", typeflag: tar.TypeSymlink, linkname: "../../../outside"},
	})
	if err := cas.ExtractTarGzSafeWithLimits(singleEscapeArchive, filepath.Join(dir, "out-single"), cas.DefaultArchiveLimits()); err == nil {
		t.Fatal("expected relative symlink escaping extraction root to be rejected")
	}

	forwardCollapseArchive := filepath.Join(dir, "forward-collapse.tar.gz")
	writeLinkTestTarGz(t, forwardCollapseArchive, []tarLinkEntry{
		{name: "a/b/c/", typeflag: tar.TypeDir},
		{name: "a/b/c/up", typeflag: tar.TypeSymlink, linkname: "../../.."},
		{name: "a/b/c/escape", typeflag: tar.TypeSymlink, linkname: "up/../../outside"},
	})
	if err := cas.ExtractTarGzSafeWithLimits(forwardCollapseArchive, filepath.Join(dir, "out-forward"), cas.DefaultArchiveLimits()); err == nil {
		t.Fatal("expected two-symlink lexical collapse traversal (forward order) to be rejected")
	}

	reverseCollapseArchive := filepath.Join(dir, "reverse-collapse.tar.gz")
	writeLinkTestTarGz(t, reverseCollapseArchive, []tarLinkEntry{
		{name: "a/b/c/", typeflag: tar.TypeDir},
		{name: "a/b/c/escape", typeflag: tar.TypeSymlink, linkname: "up/../../outside"},
		{name: "a/b/c/up", typeflag: tar.TypeSymlink, linkname: "../../.."},
	})
	if err := cas.ExtractTarGzSafeWithLimits(reverseCollapseArchive, filepath.Join(dir, "out-reverse"), cas.DefaultArchiveLimits()); err == nil {
		t.Fatal("expected two-symlink lexical collapse traversal (reverse order) to be rejected")
	}

	if !hostAllowsFileSymlinks(t) {
		return
	}

	confinedLibArchive := filepath.Join(dir, "confined-lib.tar.gz")
	writeLinkTestTarGz(t, confinedLibArchive, []tarLinkEntry{
		{name: "lib/", typeflag: tar.TypeDir},
		{name: "lib/libggml.so.0.9.4", typeflag: tar.TypeReg, body: "ELF-FAKE-SHARED-OBJ"},
		{name: "lib/libggml.so.0", typeflag: tar.TypeSymlink, linkname: "libggml.so.0.9.4"},
	})
	outConfined := filepath.Join(dir, "out-confined")
	if err := cas.ExtractTarGzSafeWithLimits(confinedLibArchive, outConfined, cas.DefaultArchiveLimits()); err != nil {
		t.Fatalf("expected confined shared library symlink inside tar.gz to extract cleanly, got %v", err)
	}
	gotBytes, err := os.ReadFile(filepath.Join(outConfined, "lib", "libggml.so.0"))
	if err != nil || string(gotBytes) != "ELF-FAKE-SHARED-OBJ" {
		t.Fatalf("unexpected confined symlink content: %q err=%v", string(gotBytes), err)
	}
}

func TestSSRFGuardBlocksZeroNetworkAndPublicRedirectToPrivateLAN(t *testing.T) {
	for _, blockedURL := range []string{
		"http://0.0.0.0:8080/models/evil.gguf",
		"http://100.64.0.10/metadata",
		"http://[64:ff9b::c0a8:0101]/internal",
	} {
		if _, _, err := cas.ValidateFetchURL(context.Background(), blockedURL); err == nil {
			t.Fatalf("expected %q to be rejected by ValidateFetchURL", blockedURL)
		}
	}

	customCalled := false
	customClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			customCalled = true
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}),
	}
	dest := filepath.Join(t.TempDir(), "blocked.bin")
	adapter := cas.NewWorkerCASAdapter(cas.NewRuntimeArchiveVerifier(), customClient)
	if _, err := adapter.DownloadVerified(context.Background(), "http://0.0.0.0:8000/model.gguf", dest, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil || customCalled {
		t.Fatalf("expected custom RoundTripper guarded against restricted address, called=%v err=%v", customCalled, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
