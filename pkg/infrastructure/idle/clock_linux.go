//go:build linux

package idle

import (
	"syscall"
	"unsafe"
)

const linuxClockMonotonic = 1

func readSystemMonotonicMicros() int64 {
	var ts syscall.Timespec
	_, _, errno := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, linuxClockMonotonic, uintptr(unsafe.Pointer(&ts)), 0)
	if errno == 0 && ts.Sec > 0 {
		return int64(ts.Sec)*1_000_000 + int64(ts.Nsec)/1_000
	}
	return readProcUptimeMicros()
}
