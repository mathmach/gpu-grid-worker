//go:build windows

package idle

import (
	"math"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type winLastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

var (
	modUser32            = windows.NewLazySystemDLL("user32.dll")
	procGetLastInputInfo = modUser32.NewProc("GetLastInputInfo")
	modKernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procGetTickCount64   = modKernel32.NewProc("GetTickCount64")

	idleMu           sync.Mutex
	hasIdleSample    bool
	lastInputTick    uint32
	lastSampleTick64 uint64
	accumulatedIdle  uint64
)

func UserIdleSeconds() int {
	var info winLastInputInfo
	info.cbSize = uint32(unsafe.Sizeof(info))
	r1, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		return -1
	}
	rTick, _, _ := procGetTickCount64.Call()
	tick64 := uint64(rTick)
	rawElapsedMs := uint64(uint32(tick64) - info.dwTime)

	idleMu.Lock()
	if !hasIdleSample || info.dwTime != lastInputTick || tick64 < lastSampleTick64 {
		hasIdleSample = true
		lastInputTick = info.dwTime
		lastSampleTick64 = tick64
		accumulatedIdle = rawElapsedMs
	} else {
		deltaMs := tick64 - lastSampleTick64
		lastSampleTick64 = tick64
		accumulatedIdle = max(accumulatedIdle+deltaMs, rawElapsedMs)
	}
	totalIdleSec := accumulatedIdle / 1000
	idleMu.Unlock()

	if totalIdleSec > uint64(math.MaxInt) {
		return math.MaxInt
	}
	return int(totalIdleSec)
}
