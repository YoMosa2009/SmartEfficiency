// Package platform defines the one seam between SmartEfficiency's OS-agnostic
// monitoring logic and the OS-specific APIs it needs to actually do anything.
// Exactly one implementation of Backend is compiled into any given build -
// platform_windows.go, platform_linux.go, or platform_darwin.go - selected by
// Go build tags at compile time, never chosen at runtime. New() (defined once
// per platform file, same signature) returns that build's implementation.
//
// Why this split exists: every mechanism SmartEfficiency relies on (process
// throttling, memory trimming, battery info, autostart registration) is a
// genuinely different OS API on each platform - there is no shared substrate
// to call once and have it work everywhere. See README.md for the full
// per-OS API mapping and which backends have actually been run and verified
// versus written-against-docs-but-untested.
package platform

import "time"

// Tier is how aggressively a process should be throttled right now.
type Tier int

const (
	TierNormal Tier = iota
	TierEfficiency
)

// BatteryInfo is a snapshot of AC/battery state.
type BatteryInfo struct {
	Present   bool // false on desktops/VMs with no battery at all
	OnBattery bool
	Percent   int // 0-100; -1 if unknown
}

// ProcInfo is a lightweight process snapshot - just enough to decide what to
// throttle, not a full process-listing API.
type ProcInfo struct {
	PID  int
	Name string
}

// EnergyIssue is one deduplicated finding from a platform-native energy
// diagnostic (Windows: powercfg /energy; Linux: TLP presence/absence).
type EnergyIssue struct {
	Severity string // "Error" | "Warning" | "Info"
	Category string
	Name     string
	Detail   string
	Count    int // how many near-identical raw findings this line represents
}

// EnergyAuditResult is the outcome of a (typically slow, sometimes
// privileged) periodic energy diagnostic - not something to run every poll
// cycle. See Backend.EnergyAudit.
type EnergyAuditResult struct {
	RanAt      time.Time
	ErrorCount int
	WarnCount  int
	TopIssues  []EnergyIssue
}

// Backend is the full set of OS-specific operations SmartEfficiency needs.
// Every method must be safe to call frequently (every poll cycle) and must
// degrade gracefully (return a zero value + error) rather than panic if the
// underlying OS call fails - a monitoring daemon must never crash because one
// signal was momentarily unavailable.
type Backend interface {
	// Name identifies the active backend for logging/UI, e.g. "windows".
	Name() string

	// RamPercent returns current system RAM usage 0-100, or -1 if unknown.
	RamPercent() (int, error)

	// Battery returns current AC/battery status.
	Battery() (BatteryInfo, error)

	// ForegroundPID returns the PID owning the currently focused window, or
	// 0 if it can't be determined. The least portable signal in this
	// interface - see per-OS notes in platform_linux.go especially.
	ForegroundPID() (int, error)

	// ListProcesses returns a snapshot of currently running processes.
	ListProcesses() ([]ProcInfo, error)

	// SetTier applies or clears efficiency-mode-style throttling on pid.
	// Must be idempotent - called every cycle with the process's current tier.
	SetTier(pid int, tier Tier) error

	// TrimMemory best-effort trims pid's resident memory. Returns bytes
	// freed if measurable, else 0. Never an error for "nothing to trim".
	TrimMemory(pid int) (int64, error)

	// TimerResolution returns the current system timer resolution in
	// milliseconds and whether it's meaningfully elevated above the
	// platform default (some process is holding a high-precision timer
	// request, which per Microsoft's own measurements can cost 10-25% extra
	// system power by keeping the CPU out of deep idle states). Cheap
	// enough to call every poll cycle. Returns (0, false) on platforms with
	// no equivalent concept exposed the same way (currently: Linux, macOS).
	TimerResolution() (ms float64, elevated bool)

	// EnergyAudit runs a platform-native energy diagnostic and returns a
	// deduplicated summary. Unlike everything else in this interface, this
	// is explicitly NOT meant to run every cycle - it's slow (Windows:
	// ~20s of active measurement) and/or privileged (Windows: requires
	// admin). Callers should run this on a long timer (daily/weekly) or on
	// explicit user request, never in the hot poll loop.
	EnergyAudit() (*EnergyAuditResult, error)

	// InstallAutostart registers the daemon+tray to run automatically in the
	// background, survive reboots/logons, and restart if killed. May require
	// elevated privileges depending on OS - implementations should return a
	// clear error rather than partially registering.
	InstallAutostart(daemonPath, trayPath string) error

	// UninstallAutostart removes whatever InstallAutostart registered.
	UninstallAutostart() error

	// StopBackground stops the running background daemon/tray (however the
	// OS's service manager tracks them), releasing any file locks on their
	// executables. Used by the self-updater before swapping binaries.
	StopBackground() error

	// StartBackground starts the background daemon/tray via whatever
	// InstallAutostart registered. Used by the self-updater after swapping
	// binaries, and available as a manual "turn it back on" action.
	StartBackground() error

	// ReplaceSelf atomically swaps the file at path with the file at
	// newPath, safely even while path's contents may still be the currently
	// executing binary. Used by the self-updater.
	ReplaceSelf(path, newPath string) error
}
