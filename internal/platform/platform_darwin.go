//go:build darwin

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// darwinBackend is written against documented macOS command-line tools
// (vm_stat, sysctl, pmset, launchctl, System Events via osascript) but has
// NOT been run on an actual Mac as part of building this project - there was
// no Mac available to verify on. Shelling out to these tools (rather than
// cgo + Mach/IOKit APIs) was a deliberate choice to keep the build cgo-free
// and easy to cross-compile from any OS; the tradeoff is slightly coarser
// data and a startup cost per call. Treat this backend as a solid starting
// point that needs real-world testing.
type darwinBackend struct{}

func New() Backend { return darwinBackend{} }

func (darwinBackend) Name() string { return "darwin" }

var vmStatLineRE = regexp.MustCompile(`^Pages (\w[\w ]*\w):\s+(\d+)\.?$`)

func (darwinBackend) RamPercent() (int, error) {
	totalOut, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return -1, err
	}
	totalBytes, err := strconv.ParseUint(strings.TrimSpace(string(totalOut)), 10, 64)
	if err != nil || totalBytes == 0 {
		return -1, fmt.Errorf("could not parse hw.memsize")
	}

	vmOut, err := exec.Command("vm_stat").Output()
	if err != nil {
		return -1, err
	}
	pageSize := uint64(4096) // vm_stat's default reporting unit on all current Apple silicon + Intel Macs
	pages := map[string]uint64{}
	for _, line := range strings.Split(string(vmOut), "\n") {
		m := vmStatLineRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		n, _ := strconv.ParseUint(m[2], 10, 64)
		pages[m[1]] = n
	}
	free := pages["free"] * pageSize
	// Approximation: "used" = total - free. Coarser than Windows' MemoryLoad
	// (doesn't account for purgeable/inactive-but-reclaimable pages the way
	// Activity Monitor's "Memory Pressure" does) but good enough for the
	// same high/low pressure tiering the rest of the system uses.
	if free > totalBytes {
		return -1, fmt.Errorf("vm_stat free exceeds hw.memsize - unexpected")
	}
	used := totalBytes - free
	return int(used * 100 / totalBytes), nil
}

var pmsetPercentRE = regexp.MustCompile(`(\d+)%`)

func (darwinBackend) Battery() (BatteryInfo, error) {
	out, err := exec.Command("pmset", "-g", "batt").Output()
	if err != nil {
		return BatteryInfo{Percent: -1}, err
	}
	text := string(out)
	info := BatteryInfo{Percent: -1}
	info.Present = strings.Contains(text, "InternalBattery")
	info.OnBattery = strings.Contains(text, "'Battery Power'")
	if m := pmsetPercentRE.FindStringSubmatch(text); m != nil {
		if pct, err := strconv.Atoi(m[1]); err == nil {
			info.Percent = pct
		}
	}
	return info, nil
}

// ForegroundPID shells out to System Events via osascript. Requires
// Accessibility/Automation permission be granted to whatever runs this
// binary (macOS will prompt on first use) - documented as a setup step in
// README, unverified end-to-end.
func (darwinBackend) ForegroundPID() (int, error) {
	out, err := exec.Command("osascript", "-e",
		`tell application "System Events" to unix id of first process whose frontmost is true`).Output()
	if err != nil {
		return 0, nil // permission not granted yet, or no GUI session - not a hard error
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, nil
	}
	return pid, nil
}

func (darwinBackend) ListProcesses() ([]ProcInfo, error) {
	out, err := exec.Command("ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return nil, err
	}
	var procs []ProcInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		procs = append(procs, ProcInfo{PID: pid, Name: filepath.Base(strings.TrimSpace(fields[1]))})
	}
	return procs, nil
}

// SetTier uses setpriority(2), same mechanism and same unverified
// restore-to-normal caveat as the Linux backend - see platform_linux.go's
// SetTier comment for the full explanation.
func (darwinBackend) SetTier(pid int, tier Tier) error {
	nice := 0
	if tier == TierEfficiency {
		nice = 10
	}
	return syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice)
}

// TrimMemory: no public, unprivileged API exists on macOS to trim another
// process's resident memory (task ports for other processes are
// entitlement-gated). Documented no-op, same reasoning as Linux.
func (darwinBackend) TrimMemory(pid int) (int64, error) {
	return 0, nil
}

// TimerResolution: no equivalent implemented for macOS yet - see README's
// platform status table. Returning (0, false) rather than a fabricated
// number.
func (darwinBackend) TimerResolution() (float64, bool) {
	return 0, false
}

// EnergyAudit: not implemented for macOS yet. macOS's analogue would be
// `powermetrics` (per-process energy impact, needs sudo) or Activity
// Monitor's Energy tab - deliberately not built without a Mac available to
// verify the parsing against real output, the same standard applied to the
// Windows implementation (which WAS verified against a real report before
// being written). Returning a clear error rather than a fake empty result.
func (darwinBackend) EnergyAudit() (*EnergyAuditResult, error) {
	return nil, fmt.Errorf("energy audit not implemented on macOS yet")
}

func launchAgentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents"), nil
}

const (
	daemonLabel = "com.smartefficiency.daemon"
	trayLabel   = "com.smartefficiency.tray"
)

func plistPath(dir, label string) string {
	return filepath.Join(dir, label+".plist")
}

func writePlist(path, label, execPath string) error {
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
`, label, execPath)
	return os.WriteFile(path, []byte(plist), 0o644)
}

func (darwinBackend) InstallAutostart(daemonPath, trayPath string) error {
	dir, err := launchAgentsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	daemonPlist := plistPath(dir, daemonLabel)
	trayPlist := plistPath(dir, trayLabel)
	if err := writePlist(daemonPlist, daemonLabel, daemonPath); err != nil {
		return err
	}
	if err := writePlist(trayPlist, trayLabel, trayPath); err != nil {
		return err
	}
	for _, p := range []string{daemonPlist, trayPlist} {
		out, err := exec.Command("launchctl", "load", "-w", p).CombinedOutput()
		if err != nil {
			return fmt.Errorf("launchctl load -w %s: %v: %s", p, err, out)
		}
	}
	return nil
}

func (darwinBackend) UninstallAutostart() error {
	dir, err := launchAgentsDir()
	if err != nil {
		return err
	}
	var firstErr error
	for _, label := range []string{daemonLabel, trayLabel} {
		p := plistPath(dir, label)
		if out, err := exec.Command("launchctl", "unload", "-w", p).CombinedOutput(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("launchctl unload -w %s: %v: %s", p, err, out)
		}
		_ = os.Remove(p)
	}
	return firstErr
}

func (darwinBackend) StopBackground() error {
	for _, label := range []string{daemonLabel, trayLabel} {
		_ = exec.Command("launchctl", "stop", label).Run()
	}
	return nil
}

func (darwinBackend) StartBackground() error {
	for _, label := range []string{daemonLabel, trayLabel} {
		_ = exec.Command("launchctl", "start", label).Run()
	}
	return nil
}

// ReplaceSelf: same reasoning as Linux - renaming over a running binary's
// path is safe on Unix-family filesystems.
func (darwinBackend) ReplaceSelf(path, newPath string) error {
	old := path + ".old"
	_ = os.Remove(old)
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, old); err != nil {
			return fmt.Errorf("rename current -> .old: %w", err)
		}
	}
	if err := os.Rename(newPath, path); err != nil {
		_ = os.Rename(old, path)
		return fmt.Errorf("rename new -> current: %w", err)
	}
	_ = os.Chmod(path, 0o755)
	_ = os.Remove(old)
	return nil
}
