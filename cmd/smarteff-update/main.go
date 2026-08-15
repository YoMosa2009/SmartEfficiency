// smarteff-update is a short-lived helper, NOT a background service. The
// daemon and tray each periodically spawn it as an independent detached
// process when they notice a new release - it is deliberately not run
// in-process by either of them, because the update needs to stop AND
// overwrite the daemon/tray binaries, and a process cannot safely do that to
// itself (on Windows specifically, stopping its own scheduled task kills it
// before it can finish swapping files). Being a separate process, stopping
// daemon+tray has no effect on this one, so it can safely finish the job.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/YoMosa2009/SmartEfficiency/internal/config"
	"github.com/YoMosa2009/SmartEfficiency/internal/platform"
	"github.com/YoMosa2009/SmartEfficiency/internal/update"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

const (
	repoOwner = "YoMosa2009"
	repoName  = "SmartEfficiency"
)

func exeExt() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func main() {
	checkOnly := flag.Bool("check", false, "only check for an update; print the result and exit (0 = up to date, 1 = update available or check failed)")
	flag.Parse()

	dir, err := config.Dir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "smarteff-update: could not resolve install directory:", err)
		os.Exit(1)
	}

	c := &update.Checker{
		Owner:          repoOwner,
		Repo:           repoName,
		CurrentVersion: version,
		Backend:        platform.New(),
		DaemonPath:     filepath.Join(dir, "smarteffd"+exeExt()),
		TrayPath:       filepath.Join(dir, "smarteff-tray"+exeExt()),
	}

	tag, hasUpdate, err := c.Latest()

	if *checkOnly {
		// Structured JSON on stdout regardless of outcome, so callers (the
		// dashboard) can parse it unambiguously instead of inferring meaning
		// from an exit code. Exit code here means only "did the check itself
		// succeed" - 0 even when no update is available.
		result := struct {
			Available bool   `json:"available"`
			Tag       string `json:"tag,omitempty"`
			Current   string `json:"current"`
			Error     string `json:"error,omitempty"`
		}{Current: version}
		if err != nil {
			result.Error = err.Error()
			json.NewEncoder(os.Stdout).Encode(result)
			os.Exit(1)
		}
		result.Available = hasUpdate
		result.Tag = tag
		json.NewEncoder(os.Stdout).Encode(result)
		return
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "smarteff-update: check failed:", err)
		os.Exit(1)
	}
	if !hasUpdate {
		fmt.Println("smarteff-update: up to date (" + version + ")")
		return
	}

	fmt.Println("smarteff-update: applying", tag, "...")
	if err := c.Apply(tag); err != nil {
		fmt.Fprintln(os.Stderr, "smarteff-update: update failed, background processes left as-is (OS restart-on-failure is the fallback):", err)
		os.Exit(1)
	}
	fmt.Println("smarteff-update: updated to", tag)
}
