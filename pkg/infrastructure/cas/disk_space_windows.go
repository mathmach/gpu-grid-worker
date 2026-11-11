//go:build windows

package cas

import "golang.org/x/sys/windows"

func availableDiskBytesExisting(path string) (uint64, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var freeCaller, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &freeCaller, &totalBytes, &totalFree); err != nil {
		return 0, err
	}
	return freeCaller, nil
}
