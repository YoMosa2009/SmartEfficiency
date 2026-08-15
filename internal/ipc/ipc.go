// Package ipc is the (deliberately minimal) communication surface between
// the daemon, the tray process, and the dashboard UI. There is no socket or
// pipe - everything goes through two JSON files in the config directory,
// the same pattern the original PowerShell version used successfully:
//   - status.json: daemon -> everyone else (read-only for consumers)
//   - config.json (see internal/config): everyone -> daemon (the daemon
//     re-reads it every poll cycle, so writing Config.Enabled=false from the
//     dashboard is how "turn it off" works - no separate command channel
//     needed)
// Both are written atomically (temp file + rename) so a reader never sees a
// half-written file mid-update.
package ipc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/YoMosa2009/SmartEfficiency/internal/config"
	"github.com/YoMosa2009/SmartEfficiency/internal/platform"
)

// Status is the daemon's live snapshot, read by the tray icon (for the
// balloon/menu) and the dashboard (for the main view).
type Status struct {
	UpdatedAt        time.Time `json:"UpdatedAt"`
	Version          string    `json:"Version"`
	Backend          string    `json:"Backend"` // "windows" / "linux" / "darwin"
	Enabled          bool      `json:"Enabled"`
	OnBattery        bool      `json:"OnBattery"`
	BatteryPercent   int       `json:"BatteryPercent"` // -1 = unknown/no battery
	RamPercent       int       `json:"RamPercent"`
	HighPressure     bool      `json:"HighPressure"`
	ThrottledCount   int       `json:"ThrottledCount"`
	RamFreedTodayMB  float64   `json:"RamFreedTodayMB"`
	ForegroundName   string    `json:"ForegroundName"`
	UpdateAvailable  string    `json:"UpdateAvailable"` // new version tag, empty if up to date
	LastError        string    `json:"LastError,omitempty"`

	TimerResolutionMs      float64                     `json:"TimerResolutionMs"`
	TimerResolutionElevated bool                       `json:"TimerResolutionElevated"` // sustained 5+ min, not a momentary blip
	EnergyAudit             *platform.EnergyAuditResult `json:"EnergyAudit,omitempty"`
}

func statusPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "status.json"), nil
}

func WriteStatus(s Status) error {
	p, err := statusPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func ReadStatus() (Status, error) {
	p, err := statusPath()
	if err != nil {
		return Status{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Status{}, err
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return Status{}, err
	}
	return s, nil
}

// --- Energy audit cache. EnergyAudit() is slow/privileged (see
// platform.Backend.EnergyAudit), so it runs as a separate periodic/on-demand
// step (cmd/smarteffd -audit, elevated) rather than in the daemon's own hot
// loop. The result is cached here as JSON; the daemon picks it up into
// Status on its next cycle, same pattern as everything else in this file.

func energyAuditPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "energy-audit.json"), nil
}

func WriteEnergyAudit(r *platform.EnergyAuditResult) error {
	p, err := energyAuditPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func ReadEnergyAudit() (*platform.EnergyAuditResult, error) {
	p, err := energyAuditPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var r platform.EnergyAuditResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// --- PID files, used by the daemon/tray mutual watchdog (see
// internal/watchdog) to check on each other without any OS-specific service
// manager query - just "read a PID, ask the platform backend if it's alive".

func pidPath(name string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".pid"), nil
}

func WritePID(name string) error {
	p, err := pidPath(name)
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// IsAlive reports whether the PID last recorded for name is still running,
// using the platform backend's process list so the check works identically
// on every OS this project supports (no per-OS "is PID alive" syscall logic
// duplicated here).
func IsAlive(backend platform.Backend, name string) bool {
	p, err := pidPath(name)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	procs, err := backend.ListProcesses()
	if err != nil {
		return false
	}
	for _, proc := range procs {
		if proc.PID == pid {
			return true
		}
	}
	return false
}
