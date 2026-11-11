//go:build windows

package workerconfig

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32Locale            = windows.NewLazySystemDLL("kernel32.dll")
	procGetUserDefaultLocaleName = modKernel32Locale.NewProc("GetUserDefaultLocaleName")
)

func detectOSLocaleName() string {
	var buf [85]uint16
	r1, _, _ := procGetUserDefaultLocaleName.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if r1 == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:])
}
