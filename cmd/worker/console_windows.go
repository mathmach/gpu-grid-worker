//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

const attachParentProcess = ^uintptr(0)

var (
	modKernel32Console = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole  = modKernel32Console.NewProc("AttachConsole")
)

func attachParentConsoleIfNeeded(args []string) {
	if len(args) == 0 {
		return
	}
	outUnavailable := isStdStreamUnavailable(windows.STD_OUTPUT_HANDLE)
	errUnavailable := isStdStreamUnavailable(windows.STD_ERROR_HANDLE)
	if !outUnavailable && !errUnavailable {
		return
	}
	r1, _, _ := procAttachConsole.Call(attachParentProcess)
	if r1 == 0 {
		return
	}
	conOut, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	if outUnavailable {
		os.Stdout = conOut
	}
	if errUnavailable {
		os.Stderr = conOut
	}
}

func isStdStreamUnavailable(stdHandle uint32) bool {
	h, err := windows.GetStdHandle(stdHandle)
	if err != nil || h == 0 || h == windows.InvalidHandle {
		return true
	}
	ft, ftErr := windows.GetFileType(h)
	return ftErr != nil || ft == windows.FILE_TYPE_UNKNOWN
}
