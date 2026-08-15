//go:build windows

package platform

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

type windowsBackend struct{}

// New returns the Windows implementation of Backend. This is the one
// implementation actually run and verified on real hardware (Surface
// Laptop 3, Windows 11) as part of building this project.
func New() Backend { return windowsBackend{} }

func (windowsBackend) Name() string { return "windows" }

func (windowsBackend) RamPercent() (int, error) {
	m, ok := globalMemoryStatusEx()
	if !ok {
		return -1, fmt.Errorf("GlobalMemoryStatusEx failed")
	}
	return int(m.MemoryLoad), nil
}

func (windowsBackend) Battery() (BatteryInfo, error) {
	s, ok := getSystemPowerStatus()
	if !ok {
		return BatteryInfo{Percent: -1}, fmt.Errorf("GetSystemPowerStatus failed")
	}
	info := BatteryInfo{
		Present:   s.BatteryFlag != 128, // 128 = BATTERY_FLAG_NO_BATTERY
		OnBattery: s.ACLineStatus == 0,  // 0 = offline (AC not connected)
		Percent:   int(s.BatteryLifePercent),
	}
	if s.BatteryLifePercent == 255 {
		info.Percent = -1 // unknown
	}
	return info, nil
}

func (windowsBackend) ForegroundPID() (int, error) {
	return getForegroundWindowPID(), nil
}

func (windowsBackend) ListProcesses() ([]ProcInfo, error) {
	return snapshotProcesses()
}

func (windowsBackend) SetTier(pid int, tier Tier) error {
	h, ok := openProcess(processSetInformation|processQueryLimitedInformation, uint32(pid))
	if !ok {
		return fmt.Errorf("OpenProcess(%d) failed", pid)
	}
	defer closeHandle(h)
	if !setPowerThrottling(h, tier == TierEfficiency) {
		return fmt.Errorf("SetProcessInformation(%d) failed", pid)
	}
	return nil
}

func (windowsBackend) TrimMemory(pid int) (int64, error) {
	h, ok := openProcess(processSetQuota|processQueryLimitedInformation|processVMRead, uint32(pid))
	if !ok {
		return 0, fmt.Errorf("OpenProcess(%d) failed", pid)
	}
	defer closeHandle(h)

	before, _ := getWorkingSetSize(h)
	if !emptyWorkingSet(h) {
		return 0, fmt.Errorf("EmptyWorkingSet(%d) failed", pid)
	}
	after, _ := getWorkingSetSize(h)
	if before > after {
		return int64(before - after), nil
	}
	return 0, nil
}

func (windowsBackend) InstallAutostart(daemonPath, trayPath string) error {
	return winInstallTasks(daemonPath, trayPath)
}

func (windowsBackend) UninstallAutostart() error {
	return winUninstallTasks()
}

func (windowsBackend) StopBackground() error {
	return winStopTasks()
}

func (windowsBackend) StartBackground() error {
	return winStartTasks()
}

// ReplaceSelf swaps path for newPath. Windows allows renaming/removing a
// currently-executing image (the loader opens it with FILE_SHARE_DELETE), so
// this works even without first killing the process - but the updater still
// stops the background tasks first regardless, both to avoid any edge-case
// file lock and so the freshly-swapped binary starts clean rather than mid-
// cycle.
func (windowsBackend) ReplaceSelf(path, newPath string) error {
	old := path + ".old"
	_ = os.Remove(old) // best-effort cleanup of any leftover from a prior update
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, old); err != nil {
			return fmt.Errorf("rename current -> .old: %w", err)
		}
	}
	if err := os.Rename(newPath, path); err != nil {
		// try to restore the original so we don't leave the install broken
		_ = os.Rename(old, path)
		return fmt.Errorf("rename new -> current: %w", err)
	}
	_ = os.Remove(old)
	return nil
}

// isAccessDenied helps InstallAutostart give a clear, actionable error
// instead of a raw Windows error code when the process isn't elevated -
// registering a scheduled task requires admin, a fact this project learned
// the hard way across several earlier sessions.
func isAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	if errno, ok := err.(syscall.Errno); ok {
		return errno == 5 // ERROR_ACCESS_DENIED
	}
	return strings.Contains(strings.ToLower(err.Error()), "access is denied")
}
