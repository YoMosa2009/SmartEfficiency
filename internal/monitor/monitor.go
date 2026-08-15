// Package monitor is the OS-agnostic core loop: decide which processes count
// as "background/idle" and apply efficiency-tier throttling + periodic
// memory trimming through the platform.Backend interface. None of this file
// touches an OS API directly - that's the whole point of the split.
package monitor

import (
	"strings"
	"time"

	"github.com/YoMosa2009/SmartEfficiency/internal/config"
	"github.com/YoMosa2009/SmartEfficiency/internal/ipc"
	"github.com/YoMosa2009/SmartEfficiency/internal/platform"
)

// builtinCritical is a conservative always-excluded list of core OS
// processes that should never be throttled, on top of whatever the user
// adds via config.ExcludedProcessNames. Matched case-insensitively,
// extension-agnostic (a Linux "systemd" matches "systemd", a Windows
// "System" matches "system", etc.).
var builtinCritical = map[string]bool{
	"system": true, "idle": true, "smss": true, "csrss": true,
	"wininit": true, "services": true, "lsass": true, "winlogon": true,
	"dwm": true, "explorer": true, "svchost": true,
	"systemd": true, "kthreadd": true, "launchd": true, "kernel_task": true,
	"windowserver": true, "loginwindow": true,
}

func normalizeName(name string) string {
	name = strings.ToLower(name)
	name = strings.TrimSuffix(name, ".exe")
	return name
}

// Loop holds the state that must persist across poll cycles (idle timers,
// today's running totals) - everything else is recomputed fresh each cycle.
type Loop struct {
	backend platform.Backend
	watcher *config.Watcher
	version string

	lastForeground map[int]time.Time
	currentTier    map[int]platform.Tier
	lastTrim       map[int]time.Time

	ramFreedTodayBytes int64
	ramFreedDay        string
}

func New(backend platform.Backend, watcher *config.Watcher, version string) *Loop {
	return &Loop{
		backend:        backend,
		watcher:        watcher,
		version:        version,
		lastForeground: map[int]time.Time{},
		currentTier:    map[int]platform.Tier{},
		lastTrim:       map[int]time.Time{},
	}
}

// Cycle runs exactly one poll iteration. Exported (not just used by Run) so
// the daemon's main loop controls timing/ticker/shutdown, and so tests can
// drive individual cycles deterministically.
func (l *Loop) Cycle() ipc.Status {
	cfg := l.watcher.Get()
	now := time.Now()

	today := now.Format("2006-01-02")
	if l.ramFreedDay != today {
		l.ramFreedDay = today
		l.ramFreedTodayBytes = 0
	}

	status := ipc.Status{
		UpdatedAt: now,
		Version:   l.version,
		Backend:   l.backend.Name(),
		Enabled:   cfg.Enabled,
	}

	if batt, err := l.backend.Battery(); err == nil {
		status.OnBattery = batt.OnBattery
		status.BatteryPercent = batt.Percent
	} else {
		status.BatteryPercent = -1
		status.LastError = "battery: " + err.Error()
	}

	if ram, err := l.backend.RamPercent(); err == nil {
		status.RamPercent = ram
		status.HighPressure = ram >= cfg.RamPressureHighPct
	} else {
		status.RamPercent = -1
	}

	if !cfg.Enabled {
		// Paused: report state but don't touch any process's priority/memory.
		status.RamFreedTodayMB = float64(l.ramFreedTodayBytes) / (1024 * 1024)
		return status
	}

	fgPID, _ := l.backend.ForegroundPID()
	if fgPID != 0 {
		l.lastForeground[fgPID] = now
	}

	idleThreshold := time.Duration(cfg.IdleSecondsBase) * time.Second
	if status.OnBattery {
		idleThreshold = time.Duration(cfg.IdleSecondsOnBattery) * time.Second
	}

	excluded := map[string]bool{}
	for _, n := range cfg.ExcludedProcessNames {
		excluded[normalizeName(n)] = true
	}

	procs, err := l.backend.ListProcesses()
	if err != nil {
		status.LastError = "list processes: " + err.Error()
		status.RamFreedTodayMB = float64(l.ramFreedTodayBytes) / (1024 * 1024)
		return status
	}

	seen := map[int]bool{}
	throttled := 0

	for _, p := range procs {
		seen[p.PID] = true
		name := normalizeName(p.Name)
		if builtinCritical[name] || excluded[name] {
			continue
		}

		isForeground := p.PID == fgPID
		lastFg, everFocused := l.lastForeground[p.PID]
		idleFor := now.Sub(lastFg)

		wantTier := platform.TierNormal
		switch {
		case isForeground:
			wantTier = platform.TierNormal
		case status.HighPressure:
			// Most-aggressive-tier-wins: under real RAM pressure, anything
			// not actively focused gets throttled immediately regardless of
			// how long it's been idle.
			wantTier = platform.TierEfficiency
		case everFocused && idleFor >= idleThreshold:
			wantTier = platform.TierEfficiency
		case !everFocused:
			// Never observed as foreground since this daemon started - treat
			// as background from the start rather than waiting a full idle
			// window out for processes that were already running.
			wantTier = platform.TierEfficiency
		}

		if wantTier == platform.TierEfficiency {
			throttled++
		}

		if l.currentTier[p.PID] != wantTier {
			if err := l.backend.SetTier(p.PID, wantTier); err == nil {
				l.currentTier[p.PID] = wantTier
			}
		}

		if wantTier == platform.TierEfficiency {
			last, ok := l.lastTrim[p.PID]
			if !ok || now.Sub(last) >= 10*time.Minute {
				l.lastTrim[p.PID] = now
				if freed, err := l.backend.TrimMemory(p.PID); err == nil {
					l.ramFreedTodayBytes += freed
				}
			}
		}
	}

	// Forget state for processes that have exited, so maps don't grow
	// unbounded over a long-running daemon's lifetime.
	for pid := range l.lastForeground {
		if !seen[pid] {
			delete(l.lastForeground, pid)
		}
	}
	for pid := range l.currentTier {
		if !seen[pid] {
			delete(l.currentTier, pid)
		}
	}
	for pid := range l.lastTrim {
		if !seen[pid] {
			delete(l.lastTrim, pid)
		}
	}

	status.ThrottledCount = throttled
	status.RamFreedTodayMB = float64(l.ramFreedTodayBytes) / (1024 * 1024)
	if fgPID != 0 {
		for _, p := range procs {
			if p.PID == fgPID {
				status.ForegroundName = p.Name
				break
			}
		}
	}
	return status
}

// Run blocks, calling Cycle on cfg's PollIntervalSeconds cadence and writing
// each result to status.json, until stop is closed.
func (l *Loop) Run(stop <-chan struct{}) {
	for {
		cfg := l.watcher.Get()
		interval := time.Duration(cfg.PollIntervalSeconds) * time.Second
		if interval <= 0 {
			interval = 15 * time.Second
		}

		status := l.Cycle()
		_ = ipc.WriteStatus(status)

		select {
		case <-stop:
			return
		case <-time.After(interval):
			l.watcher.Refresh()
		}
	}
}
