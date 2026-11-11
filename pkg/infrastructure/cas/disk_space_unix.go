//go:build !windows

package cas

import "golang.org/x/sys/unix"

func availableDiskBytesExisting(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	if int64(st.Bavail) <= 0 || int64(st.Bsize) <= 0 {
		return 0, nil
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
