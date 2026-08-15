//go:build linux

package platform

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// linuxBackend is written against documented Linux APIs/tools (/proc,
// /sys/class/power_supply, systemd --user units) but has NOT been run on an
// actual Linux machine as part of building this project - there was no
// Linux box available to verify on. Treat it as a solid starting point that
// needs real-world testing, not a proven implementation. Specific known gaps
// are called out per-method below.
type linuxBackend struct{}

func New() Backend { return linuxBackend{} }

func (linuxBackend) Name() string { return "linux" }

func (linuxBackend) RamPercent() (int, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return -1, err
	}
	defer f.Close()

	var total, avail uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		var key string
		var val uint64
		if _, err := fmt.Sscanf(line, "%s %d", &key, &val); err != nil {
			continue
		}
		switch key {
		case "MemTotal:":
			total = val
		case "MemAvailable:":
			avail = val
		}
	}
	if total == 0 {
		return -1, fmt.Errorf("could not read MemTotal from /proc/meminfo")
	}
	used := total - avail
	return int(used * 100 / total), nil
}

// Battery reads /sys/class/power_supply directly rather than shelling out to
// upower, so it works even on minimal systems without upowerd running.
func (linuxBackend) Battery() (BatteryInfo, error) {
	const base = "/sys/class/power_supply"
	entries, err := os.ReadDir(base)
	if err != nil {
		return BatteryInfo{Percent: -1}, err
	}

	info := BatteryInfo{Percent: -1}
	foundBattery := false
	onlineAC := false
	sawACSupply := false

	for _, e := range entries {
		name := e.Name()
		typePath := filepath.Join(base, name, "type")
		typeBytes, err := os.ReadFile(typePath)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(typeBytes)) {
		case "Battery":
			foundBattery = true
			if cap, err := os.ReadFile(filepath.Join(base, name, "capacity")); err == nil {
				if pct, err := strconv.Atoi(strings.TrimSpace(string(cap))); err == nil {
					info.Percent = pct
				}
			}
		case "Mains", "USB":
			sawACSupply = true
			if online, err := os.ReadFile(filepath.Join(base, name, "online")); err == nil {
				if strings.TrimSpace(string(online)) == "1" {
					onlineAC = true
				}
			}
		}
	}

	info.Present = foundBattery
	if foundBattery && sawACSupply {
		info.OnBattery = !onlineAC
	}
	return info, nil
}

// ForegroundPID: genuinely the roughest edge on Linux - there is no single
// cross-desktop API for "which window is focused" the way Windows and macOS
// each have one. This best-effort implementation shells out to xdotool,
// which only works under X11 (or XWayland) and only if installed. Under
// native Wayland there is no portable equivalent at all (each compositor
// would need its own protocol/portal integration) - this returns (0, nil)
// in that case, meaning focus-aware throttling silently no-ops rather than
// erroring, which callers should treat as "treat everything as background".
func (linuxBackend) ForegroundPID() (int, error) {
	out, err := exec.Command("xdotool", "getactivewindow", "getwindowpid").Output()
	if err != nil {
		return 0, nil // no xdotool / not X11 - not a hard error, just unavailable
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, nil
	}
	return pid, nil
}

func (linuxBackend) ListProcesses() ([]ProcInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var procs []ProcInfo
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a PID directory
		}
		comm, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err != nil {
			continue // process exited between readdir and read, or unreadable
		}
		procs = append(procs, ProcInfo{PID: pid, Name: strings.TrimSpace(string(comm))})
	}
	return procs, nil
}

// SetTier uses setpriority(2) (nice value) as the closest available analog
// to Windows EcoQoS. Known limitation, unverified: per POSIX/Linux rules, an
// unprivileged process can raise another same-UID process's nice value
// (lower its priority) freely, but LOWERING it back down (e.g. tier ->
// Normal after Efficiency) may be refused depending on the user's
// RLIMIT_NICE, which varies by distro. If restoring normal priority fails
// silently here, that's the reason - flagged in README as a known gap to
// verify on real hardware.
func (linuxBackend) SetTier(pid int, tier Tier) error {
	nice := 0
	if tier == TierEfficiency {
		nice = 15
	}
	return syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice)
}

// TrimMemory: intentionally a documented no-op. Windows' EmptyWorkingSet has
// no safe unprivileged equivalent on Linux - process_madvise(2) can advise
// another process's memory but requires CAP_SYS_PTRACE (or root), which this
// tool deliberately does not request just to trim RAM. Real memory pressure
// relief on Linux mostly comes from the kernel's own reclaim, which is
// already fairly aggressive. Returning (0, nil) rather than an error since
// "nothing to trim" is expected behavior here, not a failure.
func (linuxBackend) TrimMemory(pid int) (int64, error) {
	return 0, nil
}

func (linuxBackend) InstallAutostart(daemonPath, trayPath string) error {
	dir, err := userSystemdDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeUnit(dir, "smartefficiency-daemon.service", "SmartEfficiency background daemon", daemonPath); err != nil {
		return err
	}
	if err := writeUnit(dir, "smartefficiency-tray.service", "SmartEfficiency tray dashboard", trayPath); err != nil {
		return err
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %v: %s", err, out)
	}
	return linuxStartUnits()
}

func writeUnit(dir, filename, description, execPath string) error {
	unit := fmt.Sprintf(`[Unit]
Description=%s
After=graphical-session.target

[Service]
ExecStart=%s
Restart=always
RestartSec=5
# No execution time limit, no battery-state condition - must run
# indefinitely regardless of AC/battery, mirroring the reliability
# requirements this project established the hard way in its PowerShell version.

[Install]
WantedBy=default.target
`, description, execPath)
	return os.WriteFile(filepath.Join(dir, filename), []byte(unit), 0o644)
}

func userSystemdDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

func linuxStartUnits() error {
	for _, n := range []string{"smartefficiency-daemon.service", "smartefficiency-tray.service"} {
		out, err := exec.Command("systemctl", "--user", "enable", "--now", n).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl --user enable --now %s: %v: %s", n, err, out)
		}
	}
	return nil
}

func (linuxBackend) UninstallAutostart() error {
	dir, err := userSystemdDir()
	if err != nil {
		return err
	}
	var firstErr error
	for _, n := range []string{"smartefficiency-daemon.service", "smartefficiency-tray.service"} {
		if out, err := exec.Command("systemctl", "--user", "disable", "--now", n).CombinedOutput(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("systemctl --user disable --now %s: %v: %s", n, err, out)
		}
		_ = os.Remove(filepath.Join(dir, n))
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return firstErr
}

func (linuxBackend) StopBackground() error {
	for _, n := range []string{"smartefficiency-daemon.service", "smartefficiency-tray.service"} {
		_ = exec.Command("systemctl", "--user", "stop", n).Run()
	}
	return nil
}

func (linuxBackend) StartBackground() error {
	return linuxStartUnits()
}

// ReplaceSelf: on Linux, renaming a file over one whose contents are the
// currently-executing binary is safe and standard - the running process
// keeps its already-open inode; the new file takes effect on next launch.
// No need for Windows' rename-to-.old dance, but kept for consistency/safety.
func (linuxBackend) ReplaceSelf(path, newPath string) error {
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
