package cas

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

const (
	DefaultMaxArchiveEntryBytes int64 = 2 << 30
	DefaultMaxArchiveTotalBytes int64 = 4 << 30
	DefaultMaxDownloadBytes     int64 = 64 << 30
)

var (
	errMaxSizeExceeded = errors.New("artifact download exceeds maximum allowed size")
	errDiscardPartFile = errors.New("unrecoverable partial response")
)

type ArchiveLimits struct {
	MaxEntryBytes int64
	MaxTotalBytes int64
}

func DefaultArchiveLimits() ArchiveLimits {
	return ArchiveLimits{
		MaxEntryBytes: DefaultMaxArchiveEntryBytes,
		MaxTotalBytes: DefaultMaxArchiveTotalBytes,
	}
}

func (l ArchiveLimits) normalize() ArchiveLimits {
	out := l
	if out.MaxEntryBytes <= 0 {
		out.MaxEntryBytes = DefaultMaxArchiveEntryBytes
	}
	if out.MaxTotalBytes <= 0 {
		out.MaxTotalBytes = DefaultMaxArchiveTotalBytes
	}
	if out.MaxEntryBytes > out.MaxTotalBytes {
		out.MaxEntryBytes = out.MaxTotalBytes
	}
	return out
}

type RuntimeArtifact struct {
	FileName string `json:"fileName"`
	SHA256   string `json:"sha256"`
	SizeByte int64  `json:"sizeBytes"`
}

func ComputeFileSHA256(path string) (string, int64, error) {
	if cachedSum, cachedSize, ok := globalDigestMemo.Lookup(path); ok {
		return cachedSum, cachedSize, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer closeFileQuietly(f)

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	globalDigestMemo.Record(path, sum)
	return sum, n, nil
}

func ScanRuntimeManifest(assetsDir string) map[string]RuntimeArtifact {
	out := make(map[string]RuntimeArtifact)
	candidates := []string{
		"worker-runtime-windows-amd64.zip",
		"worker-runtime-linux-amd64.tar.gz",
		"worker-runtime-diffusion-windows-amd64.zip",
		"worker-runtime-diffusion-linux-amd64.tar.gz",
		"worker-runtime-audio-windows-amd64.zip",
		"worker-runtime-audio-linux-amd64.tar.gz",
	}
	for _, name := range candidates {
		full := filepath.Join(assetsDir, name)
		st, err := os.Stat(full)
		if err != nil || st.IsDir() {
			continue
		}
		sum, sz, err := ComputeFileSHA256(full)
		if err == nil {
			out[name] = RuntimeArtifact{FileName: name, SHA256: sum, SizeByte: sz}
		}
	}
	return out
}

func sanitizeModelFileName(fileName string) (string, error) {
	trimmed := strings.TrimSpace(fileName)
	if trimmed == "" {
		return "", derr.New(derr.CodeValidation, 400, "model file name is required", nil)
	}
	if trimmed == "." || trimmed == ".." || strings.Contains(trimmed, "..") || strings.ContainsAny(trimmed, `/\:`+"\x00") {
		return "", derr.New(derr.CodePathTraversal, 400, "invalid model file name: "+fileName, nil)
	}
	clean := filepath.Base(filepath.Clean(trimmed))
	if clean == "" || clean == "." || clean == ".." || clean != trimmed {
		return "", derr.New(derr.CodePathTraversal, 400, "invalid model file name: "+fileName, nil)
	}
	return clean, nil
}

func normalizeDigestHex(expectedSHA256 string) (string, error) {
	want := strings.ToLower(strings.TrimSpace(expectedSHA256))
	want = strings.TrimPrefix(want, "sha256:")
	if !isValidSHA256Hex(want) {
		return "", derr.New(derr.CodeDigestMismatch, 502, "mandatory SHA-256 manifest digest missing or invalid", nil)
	}
	return want, nil
}

func resolveModelsRoot(cacheRoot string) (string, error) {
	trimmed := strings.TrimSpace(cacheRoot)
	if trimmed == "" {
		return "", derr.New(derr.CodeValidation, 400, "model cache root directory is required", nil)
	}
	cleaned := filepath.Clean(trimmed)
	if strings.EqualFold(filepath.Base(cleaned), "models") {
		return cleaned, nil
	}
	return filepath.Join(cleaned, "models"), nil
}

func ensureModelCachedBounded(ctx context.Context, client *http.Client, rawURL, cacheRoot, expectedSHA256, fileName string, maxBytes int64) (string, string, error) {
	return ensureModelCachedBoundedWithProgress(ctx, client, rawURL, cacheRoot, expectedSHA256, fileName, maxBytes, nil)
}

func ensureModelCachedBoundedWithProgress(ctx context.Context, client *http.Client, rawURL, cacheRoot, expectedSHA256, fileName string, maxBytes int64, onProgress func(int64, int64)) (string, string, error) {
	want, err := normalizeDigestHex(expectedSHA256)
	if err != nil {
		return "", "", err
	}
	cleanName, err := sanitizeModelFileName(fileName)
	if err != nil {
		return "", "", err
	}
	modelsRoot, err := resolveModelsRoot(cacheRoot)
	if err != nil {
		return "", "", err
	}
	destPath, err := SafeJoin(modelsRoot, filepath.Join(want, cleanName))
	if err != nil {
		return "", "", err
	}
	if matchesCachedDigest(destPath, want) {
		return destPath, want, nil
	}
	var vErr error
	ctx, _, vErr = ValidateFetchURL(ctx, rawURL)
	if vErr != nil {
		return "", "", vErr
	}
	httpClient := wrapSafeHTTPClient(client, 30*time.Minute)
	sfKey := canonicalCASPathKey(destPath) + "|" + want
	digest, err := globalDownloadCoord.Do(sfKey, func() (string, error) {
		unlock := globalDownloadCoord.LockPath(canonicalCASPathKey(destPath))
		defer unlock()
		if matchesCachedDigest(destPath, want) {
			return want, nil
		}
		removeQuietly(destPath)
		globalDigestMemo.Invalidate(destPath)
		return downloadVerifiedUnderLock(ctx, httpClient, rawURL, destPath, want, maxBytes, onProgress)
	})
	if err != nil {
		return "", "", err
	}
	return destPath, digest, nil
}

func matchesCachedDigest(path, want string) bool {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	got, _, err := ComputeFileSHA256(path)
	return err == nil && got == want
}

func downloadVerifiedBounded(ctx context.Context, client *http.Client, rawURL, destPath, expectedSHA256 string, maxBytes int64, onProgress func(downloaded, total int64)) (string, error) {
	want, err := normalizeDigestHex(expectedSHA256)
	if err != nil {
		return "", err
	}
	ctx, _, err = ValidateFetchURL(ctx, rawURL)
	if err != nil {
		return "", err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxDownloadBytes
	}
	httpClient := wrapSafeHTTPClient(client, 10*time.Minute)
	sfKey := canonicalCASPathKey(destPath) + "|" + want
	return globalDownloadCoord.Do(sfKey, func() (string, error) {
		unlock := globalDownloadCoord.LockPath(canonicalCASPathKey(destPath))
		defer unlock()
		if matchesCachedDigest(destPath, want) {
			return want, nil
		}
		return downloadVerifiedUnderLock(ctx, httpClient, rawURL, destPath, want, maxBytes, onProgress)
	})
}

func downloadVerifiedUnderLock(ctx context.Context, client *http.Client, rawURL, destPath, want string, maxBytes int64, onProgress func(int64, int64)) (string, error) {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return "", err
	}
	canonicalPart := destPath + ".part"
	workPart := destPath + ".part." + newRandomPartSuffix()
	partialOffset := prepareWorkingPartFile(canonicalPart, workPart, maxBytes)

	resp, partialOffset, err := openDownloadStream(ctx, client, rawURL, workPart, partialOffset)
	if err != nil {
		preserveResumablePart(workPart, canonicalPart)
		return "", err
	}
	defer closeBodyQuietly(resp.Body)

	releaseSpace, err := reserveDiskHeadroomForDownload(filepath.Dir(destPath), resp.ContentLength)
	if err != nil {
		preserveResumablePart(workPart, canonicalPart)
		return "", err
	}
	defer releaseSpace()

	digest, err := writeVerifiedResponse(resp, workPart, partialOffset, maxBytes, onProgress)
	if err != nil {
		handleFailedPartWrite(workPart, canonicalPart, err)
		return "", err
	}
	if digest != want {
		removeQuietly(workPart)
		removeQuietly(canonicalPart)
		return "", derr.New(derr.CodeDigestMismatch, 502,
			fmt.Sprintf("SHA-256 digest mismatch: got %s want %s", digest, want), nil)
	}
	return commitVerifiedPartFile(workPart, canonicalPart, destPath, digest)
}

func prepareWorkingPartFile(canonicalPart, workPart string, maxBytes int64) int64 {
	sz := existingPartFileSize(canonicalPart)
	if sz <= 0 {
		return 0
	}
	if sz > maxBytes {
		removeQuietly(canonicalPart)
		return 0
	}
	if err := os.Rename(canonicalPart, workPart); err != nil {
		return 0
	}
	return sz
}

func openDownloadStream(ctx context.Context, client *http.Client, rawURL, workPart string, partialOffset int64) (*http.Response, int64, error) {
	resp, err := doRangeDownloadRequest(ctx, client, rawURL, partialOffset)
	if err != nil {
		return nil, partialOffset, err
	}
	if partialOffset > 0 && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		closeBodyQuietly(resp.Body)
		removeQuietly(workPart)
		resp2, err2 := doRangeDownloadRequest(ctx, client, rawURL, 0)
		return resp2, 0, err2
	}
	return resp, partialOffset, nil
}

func handleFailedPartWrite(workPart, canonicalPart string, err error) {
	if errors.Is(err, errMaxSizeExceeded) || errors.Is(err, errDiscardPartFile) {
		removeQuietly(workPart)
		removeQuietly(canonicalPart)
		return
	}
	preserveResumablePart(workPart, canonicalPart)
}

func preserveResumablePart(workPart, canonicalPart string) {
	if existingPartFileSize(workPart) <= 0 {
		removeQuietly(workPart)
		return
	}
	removeQuietly(canonicalPart)
	if err := os.Rename(workPart, canonicalPart); err != nil {
		removeQuietly(workPart)
	}
}

func commitVerifiedPartFile(workPart, canonicalPart, destPath, digest string) (string, error) {
	removeQuietly(destPath)
	if err := os.Rename(workPart, destPath); err != nil {
		removeQuietly(workPart)
		return "", err
	}
	removeQuietly(canonicalPart)
	globalDigestMemo.Record(destPath, digest)
	return digest, nil
}

func doRangeDownloadRequest(ctx context.Context, client *http.Client, rawURL string, partialOffset int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if partialOffset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", partialOffset))
	}
	return client.Do(req)
}

func existingPartFileSize(tmpPath string) int64 {
	st, err := os.Stat(tmpPath)
	if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 {
		return 0
	}
	return st.Size()
}

func writeVerifiedResponse(resp *http.Response, tmpPath string, partialOffset, maxBytes int64, onProgress func(int64, int64)) (string, error) {
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if partialOffset > 0 {
			if !matchesExpectedContentRange(resp.Header.Get("Content-Range"), partialOffset) {
				return "", derr.Wrap(derr.CodeUpstreamFailure, 502, "invalid Content-Range header on resumed download", errDiscardPartFile)
			}
			return appendAndHashPart(resp.Body, tmpPath, partialOffset, resp.ContentLength, maxBytes, onProgress)
		}
		return streamToFileWithHash(resp.Body, tmpPath, resp.ContentLength, maxBytes, onProgress)
	case http.StatusOK:
		return streamToFileWithHash(resp.Body, tmpPath, resp.ContentLength, maxBytes, onProgress)
	default:
		return "", fmt.Errorf("download failed with HTTP %d", resp.StatusCode)
	}
}

func matchesExpectedContentRange(rawHeader string, expectedOffset int64) bool {
	clean := strings.ToLower(strings.TrimSpace(rawHeader))
	return strings.HasPrefix(clean, fmt.Sprintf("bytes %d-", expectedOffset))
}

func isValidSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func streamToFileWithHash(src io.Reader, tmpPath string, contentLen, maxBytes int64, onProgress func(int64, int64)) (string, error) {
	if contentLen > maxBytes {
		return "", derr.Wrap(derr.CodeValidation, 413, "artifact download exceeds maximum allowed size", errMaxSizeExceeded)
	}
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if err := copyChunksIntoHash(src, f, h, 0, contentLen, maxBytes, onProgress); err != nil {
		closeFileQuietly(f)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func appendAndHashPart(src io.Reader, tmpPath string, startOffset, remLen, maxBytes int64, onProgress func(int64, int64)) (string, error) {
	if remLen > 0 && startOffset+remLen > maxBytes {
		return "", derr.Wrap(derr.CodeValidation, 413, "artifact download exceeds maximum allowed size", errMaxSizeExceeded)
	}
	h := sha256.New()
	existing, err := os.Open(tmpPath)
	if err != nil {
		return "", err
	}
	copied, err := io.Copy(h, existing)
	closeFileQuietly(existing)
	if err != nil {
		return "", err
	}

	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	totalLen := int64(-1)
	if remLen >= 0 {
		totalLen = startOffset + remLen
	}
	if err := copyChunksIntoHash(src, f, h, copied, totalLen, maxBytes, onProgress); err != nil {
		closeFileQuietly(f)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyChunksIntoHash(src io.Reader, dst *os.File, h hash.Hash, initialWritten, totalLen, maxBytes int64, onProgress func(int64, int64)) error {
	remLimit := maxBytes - initialWritten
	if remLimit < 0 {
		return derr.Wrap(derr.CodeValidation, 413, "artifact download exceeds maximum allowed size", errMaxSizeExceeded)
	}
	limited := io.LimitReader(src, remLimit+1)
	buf := make([]byte, 64*1024)
	written := initialWritten
	w := io.MultiWriter(dst, h)
	for {
		nr, rErr := limited.Read(buf)
		if nr > 0 {
			if err := writeBoundedChunk(w, buf[:nr], &written, totalLen, maxBytes, onProgress); err != nil {
				return err
			}
		}
		if rErr == io.EOF {
			return nil
		}
		if rErr != nil {
			return rErr
		}
	}
}

func writeBoundedChunk(w io.Writer, chunk []byte, written *int64, totalLen, maxBytes int64, onProgress func(int64, int64)) error {
	*written += int64(len(chunk))
	if *written > maxBytes {
		return derr.Wrap(derr.CodeValidation, 413, "artifact download exceeds maximum allowed size", errMaxSizeExceeded)
	}
	if _, err := w.Write(chunk); err != nil {
		return err
	}
	if onProgress != nil {
		onProgress(*written, totalLen)
	}
	return nil
}

func SafeJoin(destDir, entryName string) (string, error) {
	if strings.ContainsRune(entryName, '\x00') {
		return "", derr.New(derr.CodePathTraversal, 400, "invalid null byte in path", nil)
	}
	cleanBase, err := filepath.Abs(filepath.Clean(destDir))
	if err != nil {
		return "", err
	}
	normEntry := filepath.FromSlash(entryName)
	if filepath.IsAbs(normEntry) || filepath.VolumeName(normEntry) != "" || strings.HasPrefix(normEntry, string(os.PathSeparator)) {
		return "", derr.New(derr.CodePathTraversal, 400, "archive entry escapes destination directory: "+entryName, nil)
	}
	target := filepath.Join(cleanBase, normEntry)
	cleanTarget, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", err
	}
	if !isWithinAbsRoot(cleanBase, cleanTarget) {
		return "", derr.New(derr.CodePathTraversal, 400, "archive entry escapes destination directory: "+entryName, nil)
	}
	return cleanTarget, nil
}

func isWithinAbsRoot(absRoot, absTarget string) bool {
	if !strings.EqualFold(filepath.VolumeName(absRoot), filepath.VolumeName(absTarget)) {
		return false
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func ExtractZipSafeWithLimits(archivePath, destDir string, limits ArchiveLimits) error {
	normLimits := limits.normalize()
	absRoot, err := filepath.Abs(filepath.Clean(destDir))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(absRoot)
	if err != nil {
		return err
	}
	defer closeBodyQuietly(root)

	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer closeBodyQuietly(zr)

	var totalWritten int64
	for _, zf := range zr.File {
		n, err := extractZipEntryRoot(root, absRoot, zf, normLimits, totalWritten)
		if err != nil {
			return err
		}
		totalWritten += n
	}
	return nil
}

func sanitizeRelativeArchiveName(absRoot, entryName string) (string, error) {
	if _, err := SafeJoin(absRoot, entryName); err != nil {
		return "", err
	}
	rel := filepath.Clean(filepath.FromSlash(entryName))
	if rel == "." || rel == "" {
		return ".", nil
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") || strings.Contains(entryName, "..") {
		return "", derr.New(derr.CodePathTraversal, 400, "archive entry escapes destination directory: "+entryName, nil)
	}
	return rel, nil
}

func mkdirAllInRoot(root *os.Root, absRoot, relDir string) error {
	cleanDir := filepath.Clean(relDir)
	if cleanDir == "." || cleanDir == "" {
		return nil
	}
	cur := ""
	for _, part := range strings.Split(cleanDir, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		cur = joinRelativePart(cur, part)
		if err := ensureSingleDirNoSymlink(root, absRoot, cur); err != nil {
			return err
		}
	}
	return nil
}

func joinRelativePart(cur, part string) string {
	if cur == "" {
		return part
	}
	return cur + string(os.PathSeparator) + part
}

func ensureSingleDirNoSymlink(root *os.Root, absRoot, rel string) error {
	full := filepath.Join(absRoot, rel)
	if st, err := os.Lstat(full); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return derr.New(derr.CodePathTraversal, 400, "archive directory path traverses symlink or non-directory: "+rel, nil)
		}
		return nil
	}
	if err := root.Mkdir(rel, 0o755); err != nil && !os.IsExist(err) {
		return err
	}
	st, err := os.Lstat(full)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return derr.New(derr.CodePathTraversal, 400, "archive directory path traverses symlink: "+rel, nil)
	}
	return nil
}

func extractZipEntryRoot(root *os.Root, absRoot string, zf *zip.File, limits ArchiveLimits, totalSoFar int64) (int64, error) {
	relPath, err := sanitizeRelativeArchiveName(absRoot, zf.Name)
	if err != nil {
		return 0, err
	}
	mode := zf.Mode()
	if mode&os.ModeSymlink != 0 {
		return 0, derr.New(derr.CodePathTraversal, 400, "symlinks in zip archives are forbidden: "+zf.Name, nil)
	}
	if relPath == "." || zf.FileInfo().IsDir() {
		return 0, mkdirAllInRoot(root, absRoot, relPath)
	}
	if !mode.IsRegular() {
		return 0, derr.New(derr.CodePathTraversal, 400, "irregular entries in zip archives are forbidden: "+zf.Name, nil)
	}
	allowed := allowedRemainingEntryBytes(limits, totalSoFar)
	if allowed < 0 || zf.UncompressedSize64 > uint64(allowed) {
		return 0, derr.New(derr.CodeValidation, 413, "archive decompression limit exceeded", nil)
	}
	if err := mkdirAllInRoot(root, absRoot, filepath.Dir(relPath)); err != nil {
		return 0, err
	}
	rc, err := zf.Open()
	if err != nil {
		return 0, err
	}
	defer closeBodyQuietly(rc)

	return copyBoundedToRootFile(root, relPath, rc, sanitizeArchiveFileMode(mode), allowed)
}

func sanitizeArchiveFileMode(mode os.FileMode) os.FileMode {
	perm := mode.Perm() & 0o755
	if perm == 0 {
		return 0o644
	}
	return perm
}

func allowedRemainingEntryBytes(limits ArchiveLimits, totalSoFar int64) int64 {
	remTotal := limits.MaxTotalBytes - totalSoFar
	if remTotal < 0 {
		return -1
	}
	return min(limits.MaxEntryBytes, remTotal)
}

const maxTarSymlinkHops = 8

func copyBoundedToRootFile(root *os.Root, relPath string, src io.Reader, mode os.FileMode, maxAllowed int64) (int64, error) {
	if st, err := root.Lstat(relPath); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return 0, derr.New(derr.CodePathTraversal, 400, "archive entry collides with symlink: "+relPath, nil)
	}
	safePerm := sanitizeArchiveFileMode(mode)
	outFile, err := root.OpenFile(relPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, safePerm)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(outFile, io.LimitReader(src, maxAllowed+1))
	if copyErr != nil {
		closeFileQuietly(outFile)
		removeRootQuietly(root, relPath)
		return 0, copyErr
	}
	if n > maxAllowed {
		closeFileQuietly(outFile)
		removeRootQuietly(root, relPath)
		return 0, derr.New(derr.CodeValidation, 413, "archive decompression limit exceeded", nil)
	}
	if err := outFile.Close(); err != nil {
		return 0, err
	}
	return n, nil
}

func ExtractTarGzSafeWithLimits(archivePath, destDir string, limits ArchiveLimits) error {
	normLimits := limits.normalize()
	absRoot, err := filepath.Abs(filepath.Clean(destDir))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(absRoot)
	if err != nil {
		return err
	}
	defer closeBodyQuietly(root)

	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer closeFileQuietly(f)

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer closeBodyQuietly(gz)

	return extractTarStreamBounded(root, tar.NewReader(gz), absRoot, normLimits)
}

func extractTarStreamBounded(root *os.Root, tr *tar.Reader, absRoot string, limits ArchiveLimits) error {
	var totalWritten int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return verifyExtractedTreeSymlinks(absRoot)
		}
		if err != nil {
			return err
		}
		n, err := extractTarEntryRoot(root, tr, hdr, absRoot, limits, totalWritten)
		if err != nil {
			return err
		}
		totalWritten += n
	}
}

func extractTarEntryRoot(root *os.Root, tr *tar.Reader, hdr *tar.Header, absRoot string, limits ArchiveLimits, totalSoFar int64) (int64, error) {
	if hdr.Typeflag == tar.TypeXHeader || hdr.Typeflag == tar.TypeXGlobalHeader {
		return 0, nil
	}
	relPath, err := sanitizeRelativeArchiveName(absRoot, hdr.Name)
	if err != nil {
		return 0, err
	}
	if relPath == "." {
		return 0, nil
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		return 0, mkdirAllInRoot(root, absRoot, relPath)
	case tar.TypeReg, tar.TypeRegA:
		return extractTarRegularFileRoot(root, tr, hdr, absRoot, relPath, limits, totalSoFar)
	case tar.TypeSymlink:
		return 0, extractTarSymlink(root, hdr, absRoot, relPath)
	case tar.TypeLink:
		return 0, derr.New(derr.CodePathTraversal, 400, "hard links in tar archives are forbidden: "+hdr.Name, nil)
	default:
		return 0, derr.New(derr.CodePathTraversal, 400, "unsupported special tar entry type: "+hdr.Name, nil)
	}
}

func extractTarRegularFileRoot(root *os.Root, tr *tar.Reader, hdr *tar.Header, absRoot, relPath string, limits ArchiveLimits, totalSoFar int64) (int64, error) {
	allowed := allowedRemainingEntryBytes(limits, totalSoFar)
	if allowed < 0 || hdr.Size < 0 || hdr.Size > allowed {
		return 0, derr.New(derr.CodeValidation, 413, "archive decompression limit exceeded", nil)
	}
	if err := mkdirAllInRoot(root, absRoot, filepath.Dir(relPath)); err != nil {
		return 0, err
	}
	return copyBoundedToRootFile(root, relPath, tr, os.FileMode(hdr.Mode), allowed)
}

func extractTarSymlink(root *os.Root, hdr *tar.Header, absRoot, relPath string) error {
	rawTarget := hdr.Linkname
	if err := validateRelativeSymlinkTarget(rawTarget); err != nil {
		return err
	}
	if err := mkdirAllInRoot(root, absRoot, filepath.Dir(relPath)); err != nil {
		return err
	}
	evalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return err
	}
	outPath := filepath.Join(absRoot, relPath)
	evalParent, err := filepath.EvalSymlinks(filepath.Dir(outPath))
	if err != nil {
		return err
	}
	if _, err := resolveConfinedSymlinkTarget(evalRoot, evalParent, rawTarget, maxTarSymlinkHops); err != nil {
		return err
	}
	if st, lErr := root.Lstat(relPath); lErr == nil && st.IsDir() {
		return derr.New(derr.CodePathTraversal, 400, "symlink cannot replace directory: "+hdr.Name, nil)
	}
	removeRootQuietly(root, relPath)
	return os.Symlink(rawTarget, outPath)
}

func validateRelativeSymlinkTarget(rawTarget string) error {
	if strings.TrimSpace(rawTarget) == "" || strings.ContainsRune(rawTarget, '\x00') {
		return derr.New(derr.CodePathTraversal, 400, "invalid symlink target", nil)
	}
	norm := filepath.FromSlash(rawTarget)
	if filepath.IsAbs(rawTarget) || filepath.IsAbs(norm) || filepath.VolumeName(norm) != "" ||
		hasWindowsDriveOrUNCPrefix(rawTarget) || strings.HasPrefix(rawTarget, "/") || strings.HasPrefix(rawTarget, `\`) {
		return derr.New(derr.CodePathTraversal, 400, "absolute symlink target forbidden: "+rawTarget, nil)
	}
	return nil
}

func hasWindowsDriveOrUNCPrefix(p string) bool {
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//") {
		return true
	}
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func resolveConfinedSymlinkTarget(evalRoot, startDir, rawTarget string, hopsLeft int) (string, error) {
	if !isWithinAbsRoot(evalRoot, startDir) {
		return "", derr.New(derr.CodePathTraversal, 400, "symlink parent escapes destination directory", nil)
	}
	cur := filepath.Clean(startDir)
	for _, seg := range splitSymlinkSegments(rawTarget) {
		next, err := advanceConfinedSymlinkStep(evalRoot, cur, seg, hopsLeft)
		if err != nil {
			return "", err
		}
		cur = next
	}
	if !isWithinAbsRoot(evalRoot, cur) {
		return "", derr.New(derr.CodePathTraversal, 400, "symlink target escapes destination directory: "+rawTarget, nil)
	}
	if err := verifyExistingAncestorsInsideRoot(evalRoot, cur); err != nil {
		return "", err
	}
	return cur, nil
}

func splitSymlinkSegments(rawTarget string) []string {
	return strings.FieldsFunc(rawTarget, func(r rune) bool {
		return r == '/' || r == '\\'
	})
}

func advanceConfinedSymlinkStep(evalRoot, cur, seg string, hopsLeft int) (string, error) {
	if seg == "" || seg == "." {
		return cur, nil
	}
	if seg == ".." {
		return stepParentInsideRoot(evalRoot, cur)
	}
	if strings.ContainsAny(seg, `/\:`+"\x00") {
		return "", derr.New(derr.CodePathTraversal, 400, "invalid symlink segment: "+seg, nil)
	}
	cand := filepath.Join(cur, seg)
	if !isWithinAbsRoot(evalRoot, cand) {
		return "", derr.New(derr.CodePathTraversal, 400, "symlink step escapes destination directory: "+seg, nil)
	}
	return followIntermediateSymlinkIfPresent(evalRoot, cur, cand, hopsLeft)
}

func stepParentInsideRoot(evalRoot, cur string) (string, error) {
	rel, err := filepath.Rel(evalRoot, cur)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", derr.New(derr.CodePathTraversal, 400, "symlink traversal escapes destination root", nil)
	}
	next := filepath.Dir(cur)
	if !isWithinAbsRoot(evalRoot, next) {
		return "", derr.New(derr.CodePathTraversal, 400, "symlink parent step escapes destination root", nil)
	}
	return next, nil
}

func followIntermediateSymlinkIfPresent(evalRoot, parentDir, cand string, hopsLeft int) (string, error) {
	st, err := os.Lstat(cand)
	if err != nil || st.Mode()&os.ModeSymlink == 0 {
		return cand, nil
	}
	if hopsLeft <= 0 {
		return "", derr.New(derr.CodePathTraversal, 400, "symlink hop depth exceeded", nil)
	}
	linkDest, err := os.Readlink(cand)
	if err != nil {
		return "", err
	}
	if err := validateRelativeSymlinkTarget(linkDest); err != nil {
		return "", err
	}
	return resolveConfinedSymlinkTarget(evalRoot, parentDir, linkDest, hopsLeft-1)
}

func verifyExtractedTreeSymlinks(absRoot string) error {
	evalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return err
	}
	return filepath.WalkDir(evalRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink == 0 {
			return nil
		}
		if err := verifySingleExtractedSymlink(evalRoot, path); err != nil {
			removeQuietly(path)
			return err
		}
		return nil
	})
}

func verifySingleExtractedSymlink(evalRoot, linkPath string) error {
	rawTarget, err := os.Readlink(linkPath)
	if err != nil {
		return err
	}
	if err := validateRelativeSymlinkTarget(rawTarget); err != nil {
		return err
	}
	evalParent, err := filepath.EvalSymlinks(filepath.Dir(linkPath))
	if err != nil {
		return err
	}
	if _, err := resolveConfinedSymlinkTarget(evalRoot, evalParent, rawTarget, maxTarSymlinkHops); err != nil {
		return err
	}
	if _, statErr := os.Stat(linkPath); statErr == nil {
		evalDest, evalErr := filepath.EvalSymlinks(linkPath)
		if evalErr != nil {
			return evalErr
		}
		if !isWithinAbsRoot(evalRoot, evalDest) {
			return derr.New(derr.CodePathTraversal, 400, "resolved symlink escapes destination directory: "+rawTarget, nil)
		}
	}
	return nil
}

func verifyResolvedDirInsideRoot(evalRoot, dirPath string) error {
	evalDir, err := filepath.EvalSymlinks(dirPath)
	if err != nil {
		return err
	}
	if !isWithinAbsRoot(evalRoot, evalDir) {
		return derr.New(derr.CodePathTraversal, 400, "symlink target ancestor escapes destination directory", nil)
	}
	return nil
}

func verifyExistingAncestorsInsideRoot(evalRoot, targetPath string) error {
	cur := targetPath
	for {
		if _, err := os.Lstat(cur); err == nil {
			return verifyResolvedDirInsideRoot(evalRoot, cur)
		}
		if cur == evalRoot {
			return nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil
		}
		cur = parent
	}
}

func closeBodyQuietly(c io.Closer) {
	if c != nil {
		if err := c.Close(); err != nil {
			return
		}
	}
}

func closeFileQuietly(f *os.File) {
	if f != nil {
		if err := f.Close(); err != nil {
			return
		}
	}
}

func removeQuietly(p string) {
	if p != "" {
		if err := os.Remove(p); err != nil {
			return
		}
	}
}

func removeRootQuietly(root *os.Root, rel string) {
	if root != nil && rel != "" && rel != "." {
		if err := root.Remove(rel); err != nil {
			return
		}
	}
}

type RuntimeArchiveVerifier struct {
	archiveLimits    ArchiveLimits
	maxDownloadBytes int64
}

func NewRuntimeArchiveVerifier() *RuntimeArchiveVerifier {
	return &RuntimeArchiveVerifier{
		archiveLimits:    DefaultArchiveLimits(),
		maxDownloadBytes: DefaultMaxDownloadBytes,
	}
}

func (v *RuntimeArchiveVerifier) DownloadVerified(ctx context.Context, client *http.Client, rawURL, destPath, expectedSHA256 string) (string, error) {
	limit := DefaultMaxDownloadBytes
	if v != nil && v.maxDownloadBytes > 0 {
		limit = v.maxDownloadBytes
	}
	return downloadVerifiedBounded(ctx, client, rawURL, destPath, expectedSHA256, limit, nil)
}

func (v *RuntimeArchiveVerifier) ExtractArchive(archivePath, destDir string) error {
	limits := DefaultArchiveLimits()
	if v != nil {
		limits = v.archiveLimits.normalize()
	}
	if strings.HasSuffix(strings.ToLower(archivePath), ".zip") {
		return ExtractZipSafeWithLimits(archivePath, destDir, limits)
	}
	return ExtractTarGzSafeWithLimits(archivePath, destDir, limits)
}

func (v *RuntimeArchiveVerifier) EnsureModelCached(ctx context.Context, client *http.Client, rawURL, cacheRoot, expectedSHA256, fileName string) (string, string, error) {
	limit := DefaultMaxDownloadBytes
	if v != nil && v.maxDownloadBytes > 0 {
		limit = v.maxDownloadBytes
	}
	return ensureModelCachedBounded(ctx, client, rawURL, cacheRoot, expectedSHA256, fileName, limit)
}
