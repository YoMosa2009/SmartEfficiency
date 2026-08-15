//go:build windows

package platform

import (
	"syscall"
	"unsafe"
)

// Raw DLL bindings. Kept in one file, hand-written against stdlib syscall
// rather than pulling in golang.org/x/sys/windows, so the Windows backend has
// zero external dependencies - every one of these calls was already
// validated working (same struct layouts, same functions) in the original
// PowerShell version's C# P/Invoke this project built up over several
// sessions; this is a direct port, not new territory.

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	psapi    = syscall.NewLazyDLL("psapi.dll")

	procGlobalMemoryStatusEx  = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemPowerStatus  = kernel32.NewProc("GetSystemPowerStatus")
	procOpenProcess           = kernel32.NewProc("OpenProcess")
	procCloseHandle           = kernel32.NewProc("CloseHandle")
	procSetProcessInformation = kernel32.NewProc("SetProcessInformation")
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW       = kernel32.NewProc("Process32FirstW")
	procProcess32NextW        = kernel32.NewProc("Process32NextW")

	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")

	procEmptyWorkingSet      = psapi.NewProc("EmptyWorkingSet")
	procGetProcessMemoryInfo = psapi.NewProc("GetProcessMemoryInfo")
)

const (
	processQueryLimitedInformation = 0x1000
	processSetInformation          = 0x0200
	processSetQuota                = 0x0100
	processVMRead                  = 0x0010
	th32csSnapProcess              = 0x00000002

	processPowerThrottlingExecutionSpeed = 0x1
	processInformationClassThrottling    = 4 // ProcessPowerThrottling
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

type processPowerThrottlingState struct {
	Version     uint32
	ControlMask uint32
	StateMask   uint32
}

type processMemoryCounters struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// processEntry32 mirrors PROCESSENTRY32W (wide-char exe name, fixed 260 buf).
type processEntry32 struct {
	Size              uint32
	CntUsage          uint32
	ProcessID         uint32
	DefaultHeapID     uintptr
	ModuleID          uint32
	CntThreads        uint32
	ParentProcessID   uint32
	PriClassBase      int32
	Flags             uint32
	ExeFile           [260]uint16
}

func globalMemoryStatusEx() (memoryStatusEx, bool) {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	ret, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	return m, ret != 0
}

func getSystemPowerStatus() (systemPowerStatus, bool) {
	var s systemPowerStatus
	ret, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s)))
	return s, ret != 0
}

func openProcess(access uint32, pid uint32) (syscall.Handle, bool) {
	h, _, _ := procOpenProcess.Call(uintptr(access), 0, uintptr(pid))
	if h == 0 {
		return 0, false
	}
	return syscall.Handle(h), true
}

func closeHandle(h syscall.Handle) {
	procCloseHandle.Call(uintptr(h))
}

func setPowerThrottling(h syscall.Handle, enable bool) bool {
	state := processPowerThrottlingState{
		Version:     1,
		ControlMask: processPowerThrottlingExecutionSpeed,
	}
	if enable {
		state.StateMask = processPowerThrottlingExecutionSpeed
	}
	ret, _, _ := procSetProcessInformation.Call(
		uintptr(h),
		uintptr(processInformationClassThrottling),
		uintptr(unsafe.Pointer(&state)),
		unsafe.Sizeof(state),
	)
	return ret != 0
}

func emptyWorkingSet(h syscall.Handle) bool {
	ret, _, _ := procEmptyWorkingSet.Call(uintptr(h))
	return ret != 0
}

func getWorkingSetSize(h syscall.Handle) (uintptr, bool) {
	var pmc processMemoryCounters
	pmc.cb = uint32(unsafe.Sizeof(pmc))
	ret, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.cb))
	return pmc.WorkingSetSize, ret != 0
}

func getForegroundWindowPID() int {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return int(pid)
}

func snapshotProcesses() ([]ProcInfo, error) {
	h, _, err := procCreateToolhelp32Snapshot.Call(uintptr(th32csSnapProcess), 0)
	if h == 0 || h == uintptr(^uintptr(0)) {
		return nil, err
	}
	handle := syscall.Handle(h)
	defer closeHandle(handle)

	var procs []ProcInfo
	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32FirstW.Call(uintptr(handle), uintptr(unsafe.Pointer(&entry)))
	for ret != 0 {
		procs = append(procs, ProcInfo{
			PID:  int(entry.ProcessID),
			Name: syscall.UTF16ToString(entry.ExeFile[:]),
		})
		ret, _, _ = procProcess32NextW.Call(uintptr(handle), uintptr(unsafe.Pointer(&entry)))
	}
	return procs, nil
}
