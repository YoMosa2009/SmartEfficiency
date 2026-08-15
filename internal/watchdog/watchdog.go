// Package watchdog implements SmartEfficiency's "must never silently stay
// down" requirement without a third scheduled task/service. The daemon and
// tray process each watch the other's PID file and relaunch it directly if
// it's gone - simpler than the original PowerShell version's separate
// Watchdog task, made possible because Go processes can just exec.Start a
// sibling binary directly instead of going through the OS scheduler to
// restart something.
//
// This is a safety net on top of (not a replacement for) each OS's own
// restart-on-failure setting (Task Scheduler RestartOnFailure, systemd
// Restart=always, launchd KeepAlive) - belt and suspenders, since the
// original project found the OS-level restart alone wasn't always reliable
// in practice.
package watchdog

import (
	"os/exec"
	"time"

	"github.com/YoMosa2009/SmartEfficiency/internal/ipc"
	"github.com/YoMosa2009/SmartEfficiency/internal/platform"
)

// Watch periodically checks whether the process identified by
// otherPIDName is alive, and relaunches otherExecPath if not. Blocks until
// stop is closed - run it in its own goroutine.
func Watch(backend platform.Backend, otherPIDName, otherExecPath string, interval time.Duration, stop <-chan struct{}) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if !ipc.IsAlive(backend, otherPIDName) {
				// Best-effort, deliberately not fatal: if this fails, the
				// OS-level restart-on-failure setting is still the primary
				// safety net, and the next tick tries again.
				_ = exec.Command(otherExecPath).Start()
			}
		}
	}
}
