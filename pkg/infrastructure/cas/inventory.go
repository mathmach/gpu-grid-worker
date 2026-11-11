package cas

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func rawAvailableDiskBytes(dir string) (uint64, error) {
	probePath, err := resolveExistingDiskProbePath(dir)
	if err != nil {
		return 0, err
	}
	return availableDiskBytesExisting(probePath)
}

func AvailableDiskBytes(dir string) (uint64, error) {
	freeBytes, err := rawAvailableDiskBytes(dir)
	if err != nil {
		return 0, err
	}
	reserved := currentReservedDiskBytes()
	if reserved >= freeBytes {
		return 0, nil
	}
	return freeBytes - reserved, nil
}

func AvailableDiskMiB(dir string) (int, error) {
	freeBytes, err := AvailableDiskBytes(dir)
	if err != nil {
		return 0, err
	}
	const bytesPerMiB = 1024 * 1024
	mib := freeBytes / bytesPerMiB
	const maxInt = uint64(^uint(0) >> 1)
	if mib > maxInt {
		return int(maxInt), nil
	}
	return int(mib), nil
}

func resolveExistingDiskProbePath(dir string) (string, error) {
	cleaned := strings.TrimSpace(dir)
	if cleaned == "" {
		cleaned = "."
	}
	abs, err := filepath.Abs(filepath.Clean(cleaned))
	if err != nil {
		return "", err
	}
	cur := abs
	for {
		if _, statErr := os.Stat(cur); statErr == nil {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return cur, nil
		}
		cur = parent
	}
}

func ListCachedDigests(cacheRoot string) ([]string, error) {
	modelsRoot, err := resolveModelsRoot(cacheRoot)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(modelsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	digests := make([]string, 0, len(entries))
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		digest := strings.ToLower(strings.TrimSpace(ent.Name()))
		if !isValidSHA256Hex(digest) {
			continue
		}
		if dirContainsVerifiedArtifact(filepath.Join(modelsRoot, ent.Name())) {
			digests = append(digests, digest)
		}
	}
	slices.Sort(digests)
	return digests, nil
}

func dirContainsVerifiedArtifact(dirPath string) bool {
	files, err := os.ReadDir(dirPath)
	if err != nil {
		return false
	}
	for _, f := range files {
		if f.IsDir() || isIncompleteTempName(f.Name()) {
			continue
		}
		info, infoErr := f.Info()
		if infoErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return true
		}
	}
	return false
}

func isIncompleteTempName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.HasSuffix(lower, ".part") || strings.Contains(lower, ".part.") || strings.HasSuffix(lower, ".tmp")
}
