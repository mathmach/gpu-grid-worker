//go:build windows

package llama

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	createSuspendedFlag               = 0x00000004
	createNoWindowFlag                = 0x08000000
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x00002000
	processTerminateAccess            = 0x0001
	processSetQuotaAccess             = 0x0100
)

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobObjectIOCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectExtendedLimitInfo struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                jobObjectIOCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

var (
	modKernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procCreateJobObjectW         = modKernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = modKernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = modKernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = modKernel32.NewProc("TerminateJobObject")
	procCloseHandle              = modKernel32.NewProc("CloseHandle")

	jobRegistryMu sync.Mutex
	jobRegistry   = make(map[*exec.Cmd]syscall.Handle)
)

func PrepareSubprocess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindowFlag,
	}
}

func prepareSupervisedSubprocess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindowFlag | createSuspendedFlag,
	}
}

func attachSubprocess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return errors.New("subprocess handle unavailable after start")
	}
	pid := uint32(cmd.Process.Pid)
	if !registerKillOnCloseJob(cmd, pid) {
		killProcessDirect(cmd)
		terminateProcessTree(cmd)
		return errors.New("failed to attach suspended subprocess to Windows kill-on-close job object")
	}
	if !resumeSuspendedProcessThreads(pid) {
		terminateProcessTree(cmd)
		return errors.New("failed to resume suspended subprocess threads")
	}
	return nil
}

func registerKillOnCloseJob(cmd *exec.Cmd, pid uint32) bool {
	job, ok := createKillOnCloseJobObject()
	if !ok {
		return false
	}
	if !assignPIDToJobObject(job, pid) {
		closeWinHandle(job)
		return false
	}
	jobRegistryMu.Lock()
	if prev, exists := jobRegistry[cmd]; exists {
		closeWinHandle(prev)
	}
	jobRegistry[cmd] = job
	jobRegistryMu.Unlock()
	return true
}

func resumeSuspendedProcessThreads(pid uint32) bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return false
	}
	defer closeWinHandle(syscall.Handle(snap))

	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Thread32First(snap, &entry); err != nil {
		return false
	}
	resumed := false
	for {
		if entry.OwnerProcessID == pid && resumeSingleThread(entry.ThreadID) {
			resumed = true
		}
		if err := windows.Thread32Next(snap, &entry); err != nil {
			break
		}
	}
	return resumed
}

func resumeSingleThread(threadID uint32) bool {
	th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, threadID)
	if err != nil {
		return false
	}
	defer closeWinHandle(syscall.Handle(th))
	_, resumeErr := windows.ResumeThread(th)
	return resumeErr == nil
}

func releaseSubprocess(cmd *exec.Cmd) {
	if job, ok := detachJobHandle(cmd); ok {
		closeWinHandle(job)
	}
}

func terminateProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if job, ok := detachJobHandle(cmd); ok {
		terminated, _, _ := procTerminateJobObject.Call(uintptr(job), 1)
		closeWinHandle(job)
		if terminated != 0 {
			return
		}
	}
	terminateWithTaskkillFallback(cmd)
}

func killProcessDirect(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := cmd.Process.Kill(); err != nil {
		return
	}
}

func createKillOnCloseJobObject() (syscall.Handle, bool) {
	r1, _, _ := procCreateJobObjectW.Call(0, 0)
	if r1 == 0 {
		return 0, false
	}
	job := syscall.Handle(r1)
	var info jobObjectExtendedLimitInfo
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	ok, _, _ := procSetInformationJobObject.Call(
		uintptr(job),
		jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if ok == 0 {
		closeWinHandle(job)
		return 0, false
	}
	return job, true
}

func assignPIDToJobObject(job syscall.Handle, pid uint32) bool {
	procHandle, err := syscall.OpenProcess(processTerminateAccess|processSetQuotaAccess, false, pid)
	if err != nil {
		return false
	}
	assigned, _, _ := procAssignProcessToJobObject.Call(uintptr(job), uintptr(procHandle))
	closeWinHandle(procHandle)
	return assigned != 0
}

func detachJobHandle(cmd *exec.Cmd) (syscall.Handle, bool) {
	if cmd == nil {
		return 0, false
	}
	jobRegistryMu.Lock()
	job, ok := jobRegistry[cmd]
	if ok {
		delete(jobRegistry, cmd)
	}
	jobRegistryMu.Unlock()
	return job, ok
}

func closeWinHandle(h syscall.Handle) {
	if h == 0 || h == syscall.InvalidHandle {
		return
	}
	r, _, _ := procCloseHandle.Call(uintptr(h))
	if r == 0 {
		return
	}
}

func terminateWithTaskkillFallback(cmd *exec.Cmd) {
	pid := cmd.Process.Pid
	if pid <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	killCmd := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	PrepareSubprocess(killCmd)
	if err := killCmd.Run(); err != nil {
		killProcessDirect(cmd)
	}
}
