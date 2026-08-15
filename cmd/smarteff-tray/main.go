// smarteff-tray is the user-facing half of SmartEfficiency: a tray icon plus
// a "dashboard" - a small HTML page served on 127.0.0.1 (never exposed off
// the machine) and opened in the user's default browser. A real embedded
// native window (via a webview) was the original plan, but that needs cgo on
// every platform and can't be verified here beyond Windows; a local page in
// the default browser gets the same live-data/on-off-toggle UI with zero
// cgo, so it cross-compiles as easily as the rest of this project. See
// README for the full reasoning.
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"fyne.io/systray"

	"github.com/YoMosa2009/SmartEfficiency/internal/config"
	"github.com/YoMosa2009/SmartEfficiency/internal/ipc"
	"github.com/YoMosa2009/SmartEfficiency/internal/platform"
	"github.com/YoMosa2009/SmartEfficiency/internal/trayicon"
	"github.com/YoMosa2009/SmartEfficiency/internal/watchdog"
	"github.com/YoMosa2009/SmartEfficiency/ui/dashboard"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func exeExt() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

var dashboardURL string

func main() {
	if err := ipc.WritePID("smarteff-tray"); err != nil {
		fmt.Fprintln(os.Stderr, "smarteff-tray: could not write pid file:", err)
	}

	dir, err := config.Dir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "smarteff-tray: fatal: could not resolve state directory:", err)
		os.Exit(1)
	}
	backend := platform.New()

	stop := make(chan struct{})
	daemonPath := filepath.Join(dir, "smarteffd"+exeExt())
	go watchdog.Watch(backend, "smarteffd", daemonPath, 5*time.Minute, stop)

	port, err := startServer(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "smarteff-tray: fatal: could not start local dashboard server:", err)
		os.Exit(1)
	}
	dashboardURL = fmt.Sprintf("http://127.0.0.1:%d/", port)

	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetIcon(iconForPlatform())
	systray.SetTitle("")
	systray.SetTooltip("SmartEfficiency")

	mOpen := systray.AddMenuItem("Open Dashboard", "View live status and settings")
	systray.AddSeparator()
	mToggle := systray.AddMenuItem("Pause monitoring", "Temporarily stop throttling")
	systray.AddSeparator()
	mExit := systray.AddMenuItem("Exit dashboard", "Closes this tray icon - the background monitor keeps running")

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openBrowser(dashboardURL)
			case <-mToggle.ClickedCh:
				cfg, err := config.Load()
				if err == nil {
					cfg.Enabled = !cfg.Enabled
					_ = config.Save(cfg)
					if cfg.Enabled {
						mToggle.SetTitle("Pause monitoring")
					} else {
						mToggle.SetTitle("Resume monitoring")
					}
				}
			case <-mExit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	// Reflect config.json's current Enabled state in the menu label at
	// startup too (not just after a click), and open the dashboard once so
	// first-run feels like something actually happened.
	if cfg, err := config.Load(); err == nil && !cfg.Enabled {
		mToggle.SetTitle("Resume monitoring")
	}
}

func onExit() {}

func iconForPlatform() []byte {
	if runtime.GOOS == "windows" {
		return trayicon.ICO()
	}
	return trayicon.PNG()
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// startServer binds to 127.0.0.1 on an OS-assigned free port (never 0.0.0.0
// - this must never be reachable from the network, only from this machine's
// own browser) and returns the port it's listening on.
func startServer(dir string) (int, error) {
	assetsFS, err := fs.Sub(dashboard.Assets, "assets")
	if err != nil {
		return 0, err
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(assetsFS)))
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/enabled", handleEnabled)
	mux.HandleFunc("/api/check-update", handleCheckUpdate(dir))
	mux.HandleFunc("/api/apply-update", handleApplyUpdate(dir))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	go http.Serve(ln, mux)
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	s, err := ipc.ReadStatus()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

func handleEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cfg.Enabled = body.Enabled
	if err := config.Save(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleCheckUpdate(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		updaterPath := filepath.Join(dir, "smarteff-update"+exeExt())
		out, _ := exec.Command(updaterPath, "-check").Output()
		w.Header().Set("Content-Type", "application/json")
		if len(out) == 0 {
			json.NewEncoder(w).Encode(map[string]string{"error": "updater helper not available"})
			return
		}
		w.Write(out)
	}
}

func handleApplyUpdate(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		updaterPath := filepath.Join(dir, "smarteff-update"+exeExt())
		// Detached, fire-and-forget - see cmd/smarteff-update for why this
		// can't run in-process. The dashboard's next status poll (or the
		// tray icon reappearing after a moment) is the feedback signal.
		_ = exec.Command(updaterPath).Start()
		w.WriteHeader(http.StatusAccepted)
	}
}
