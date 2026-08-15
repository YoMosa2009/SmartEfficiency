// smarteffd is the SmartEfficiency background daemon: the OS-agnostic
// monitor loop from internal/monitor, wired up to a real platform.Backend,
// with a watchdog goroutine keeping the tray process alive and a periodic
// self-update check. No UI, no console window (built with -H=windowsgui on
// Windows - see .github/workflows/release.yml) - status is communicated
// purely through status.json for the tray/dashboard to read.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/YoMosa2009/SmartEfficiency/internal/config"
	"github.com/YoMosa2009/SmartEfficiency/internal/ipc"
	"github.com/YoMosa2009/SmartEfficiency/internal/monitor"
	"github.com/YoMosa2009/SmartEfficiency/internal/platform"
	"github.com/YoMosa2009/SmartEfficiency/internal/watchdog"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func exeExt() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func main() {
	install := flag.Bool("install", false, "register daemon+tray to run automatically at logon, then exit (used by the install scripts; may require running elevated)")
	uninstall := flag.Bool("uninstall", false, "remove the autostart registration created by -install, then exit")
	flag.Parse()

	if *install || *uninstall {
		dir, err := config.Dir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "smarteffd: could not resolve state directory:", err)
			os.Exit(1)
		}
		backend := platform.New()
		if *uninstall {
			if err := backend.UninstallAutostart(); err != nil {
				fmt.Fprintln(os.Stderr, "smarteffd: uninstall failed:", err)
				os.Exit(1)
			}
			fmt.Println("smarteffd: autostart removed")
			return
		}
		daemonPath := filepath.Join(dir, "smarteffd"+exeExt())
		trayPath := filepath.Join(dir, "smarteff-tray"+exeExt())
		if err := backend.InstallAutostart(daemonPath, trayPath); err != nil {
			fmt.Fprintln(os.Stderr, "smarteffd: install failed:", err)
			os.Exit(1)
		}
		fmt.Println("smarteffd: installed and started")
		return
	}

	if err := ipc.WritePID("smarteffd"); err != nil {
		// Not fatal - the watchdog relationship degrades (tray can't verify
		// this process specifically) but core monitoring still works.
		fmt.Fprintln(os.Stderr, "smarteffd: could not write pid file:", err)
	}

	dir, err := config.Dir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "smarteffd: fatal: could not resolve state directory:", err)
		os.Exit(1)
	}

	backend := platform.New()
	watcher := config.NewWatcher()
	loop := monitor.New(backend, watcher, version)

	stop := make(chan struct{})

	// Watch the tray process; relaunch it if it disappears. Belt-and-
	// suspenders on top of the OS's own restart-on-failure setting.
	trayPath := filepath.Join(dir, "smarteff-tray"+exeExt())
	go watchdog.Watch(backend, "smarteff-tray", trayPath, 5*time.Minute, stop)

	// Periodic self-update check: spawn the standalone updater helper as a
	// detached process (never applied in-process - see cmd/smarteff-update
	// for why). Runs once at startup, then on AutoUpdateCheckIntervalHours.
	go runAutoUpdateLoop(watcher, dir, stop)

	// Graceful shutdown on SIGINT/SIGTERM. On Windows this mostly matters if
	// someone runs the daemon manually in a console; the scheduled task's
	// normal stop path (schtasks /End) uses TerminateProcess, which no
	// process can intercept - Task Scheduler's own RestartOnFailure or the
	// tray's watchdog goroutine is what brings it back in that case.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		close(stop)
	}()

	loop.Run(stop)
}

func runAutoUpdateLoop(watcher *config.Watcher, dir string, stop <-chan struct{}) {
	updaterPath := filepath.Join(dir, "smarteff-update"+exeExt())

	check := func() {
		cfg := watcher.Get()
		if !cfg.AutoUpdateEnabled {
			return
		}
		if _, err := os.Stat(updaterPath); err != nil {
			return // updater helper not installed (e.g. built from source without it) - skip quietly
		}
		// Detached, fire-and-forget: the updater is an independent process
		// that stops/restarts the daemon+tray itself if it finds and applies
		// an update. We don't wait for it or care about its exit code here.
		_ = exec.Command(updaterPath).Start()
	}

	check() // once at startup
	for {
		cfg := watcher.Get()
		hours := cfg.AutoUpdateCheckIntervalHours
		if hours <= 0 {
			hours = 24
		}
		select {
		case <-stop:
			return
		case <-time.After(time.Duration(hours) * time.Hour):
			check()
		}
	}
}
