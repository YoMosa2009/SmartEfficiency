// Package config loads and saves SmartEfficiency's user-editable settings.
// Same philosophy as the original PowerShell version: one human-readable JSON
// file, reloaded live so changes take effect without a restart, with safe
// fallback defaults if the file is missing or unreadable.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Config mirrors the fields used by the original PowerShell config.json,
// plus a couple of new fields for the Go version (update channel, enabled).
type Config struct {
	Enabled                  bool `json:"Enabled"`
	PollIntervalSeconds      int  `json:"PollIntervalSeconds"`
	IdleSecondsBase          int  `json:"IdleSecondsBase"`
	IdleSecondsOnBattery     int  `json:"IdleSecondsOnBattery"`
	RamPressureHighPct       int  `json:"RamPressureHighPct"`
	StartupScanIntervalSeconds int `json:"StartupScanIntervalSeconds"`
	WakeAuditIntervalSeconds  int  `json:"WakeAuditIntervalSeconds"`
	ChargeNudgeEnabled        bool `json:"ChargeNudgeEnabled"`
	ChargeNudgePercent        int  `json:"ChargeNudgePercent"`
	AutoUpdateEnabled         bool `json:"AutoUpdateEnabled"`
	AutoUpdateCheckIntervalHours int `json:"AutoUpdateCheckIntervalHours"`
	ExcludedProcessNames      []string `json:"ExcludedProcessNames"`
}

// Default returns safe fallback settings, used both as the on-disk default
// for a fresh install and as the in-memory fallback if the file on disk is
// ever unreadable/corrupt mid-run.
func Default() Config {
	return Config{
		Enabled:                     true,
		PollIntervalSeconds:         15,
		IdleSecondsBase:             120,
		IdleSecondsOnBattery:        60,
		RamPressureHighPct:          85,
		StartupScanIntervalSeconds:  300,
		WakeAuditIntervalSeconds:    3600,
		ChargeNudgeEnabled:          true,
		ChargeNudgePercent:          80,
		AutoUpdateEnabled:           true,
		AutoUpdateCheckIntervalHours: 24,
		ExcludedProcessNames: []string{
			"ProcessLasso", "ProcessGovernor", "smarteffd", "smarteff-tray",
		},
	}
}

// Dir returns the per-OS base directory SmartEfficiency stores its state in:
//   Windows: %LOCALAPPDATA%\SmartEfficiency
//   Linux:   $XDG_DATA_HOME/smartefficiency (falls back to ~/.local/share/smartefficiency)
//   macOS:   ~/Library/Application Support/SmartEfficiency
func Dir() (string, error) {
	dir, err := baseDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads config.json, writing a fresh default file if none exists yet.
// On any read/parse error it logs nothing itself (callers own logging) and
// returns Default() so a corrupt file degrades to safe behavior instead of
// crashing the daemon.
func Load() (Config, error) {
	p, err := path()
	if err != nil {
		return Default(), err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		def := Default()
		_ = Save(def) // best-effort; if this fails we still return usable defaults
		return def, nil
	}
	if err != nil {
		return Default(), err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Default(), err
	}
	if c.PollIntervalSeconds <= 0 {
		return Default(), nil // treat a nonsensical/half-written file as invalid
	}
	return c, nil
}

// Save atomically writes cfg to config.json (temp file + rename, so a reader
// never sees a half-written file).
func Save(cfg Config) error {
	p, err := path()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// watcher provides simple poll-based live reload (mirrors the original
// "reloaded live every cycle" behavior) without needing a filesystem-events
// dependency, which varies more across platforms.
type Watcher struct {
	mu   sync.RWMutex
	cur  Config
}

func NewWatcher() *Watcher {
	c, _ := Load()
	return &Watcher{cur: c}
}

func (w *Watcher) Get() Config {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.cur
}

// Refresh re-reads config.json from disk. Call once per poll cycle.
func (w *Watcher) Refresh() {
	c, err := Load()
	if err != nil {
		return // keep last-known-good on error
	}
	w.mu.Lock()
	w.cur = c
	w.mu.Unlock()
}
