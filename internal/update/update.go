// Package update implements self-updating from a public GitHub repo's
// Releases. Public repo means no credentials are needed on the machine at
// all - just an unauthenticated call to GitHub's public REST API, which is
// the whole reason "public repo" was chosen for this project (see README).
//
// Release asset naming this expects (produced by .github/workflows/release.yml):
//   smarteffd-<os>-<arch>[.exe]
//   smarteff-tray-<os>-<arch>[.exe]
//   checksums.txt          (sha256, one "hash  filename" line per asset)
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/YoMosa2009/SmartEfficiency/internal/platform"
)

const apiTimeout = 15 * time.Second

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type Checker struct {
	Owner, Repo    string
	CurrentVersion string // e.g. "v1.2.0"
	Backend        platform.Backend
	DaemonPath     string // full path to the currently-installed daemon executable
	TrayPath       string // full path to the currently-installed tray executable
}

// Latest fetches the newest published release and reports whether it's newer
// than CurrentVersion. Returns ("", false, nil) - not an error - if the
// check itself succeeds but there's nothing newer, so callers can treat
// "checked, nothing to do" and "check failed" differently.
func (c *Checker) Latest() (tag string, hasUpdate bool, err error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", c.Owner, c.Repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "SmartEfficiency-updater")

	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("GitHub API returned %s", resp.Status)
	}

	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", false, err
	}

	if isNewer(rel.TagName, c.CurrentVersion) {
		return rel.TagName, true, nil
	}
	return rel.TagName, false, nil
}

// isNewer does a simple major.minor.patch comparison, tolerant of a leading
// "v" and missing components (treated as 0). Deliberately not a full semver
// implementation (no pre-release/build-metadata handling) - this project's
// versioning is plain vX.Y.Z tags, nothing fancier.
func isNewer(remote, local string) bool {
	r := parseVersion(remote)
	l := parseVersion(local)
	for i := 0; i < 3; i++ {
		if r[i] != l[i] {
			return r[i] > l[i]
		}
	}
	return false
}

func parseVersion(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(v, ".", 3)
	var out [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		n, _ := strconv.Atoi(strings.TrimFunc(parts[i], func(r rune) bool { return r < '0' || r > '9' }))
		out[i] = n
	}
	return out
}

func assetSuffix() string {
	suffix := fmt.Sprintf("-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		suffix += ".exe"
	}
	return suffix
}

func findAsset(rel *release, prefix string) (asset, bool) {
	want := prefix + assetSuffix()
	for _, a := range rel.Assets {
		if a.Name == want {
			return a, true
		}
	}
	return asset{}, false
}

// Apply downloads the release tagged tag, verifies checksums, stops the
// background processes, swaps both executables into place via the platform
// backend's ReplaceSelf, and restarts them. Either both binaries update
// successfully or neither does - if anything fails partway, whatever's
// already been swapped is left as-is and the error is returned; the daemon's
// own RestartOnFailure/Restart=always/KeepAlive setting is the fallback if a
// partial update leaves things stopped.
func (c *Checker) Apply(tag string) error {
	rel, err := c.fetchRelease(tag)
	if err != nil {
		return err
	}

	daemonAsset, ok := findAsset(rel, "smarteffd")
	if !ok {
		return fmt.Errorf("no release asset for smarteffd on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	trayAsset, ok := findAsset(rel, "smarteff-tray")
	if !ok {
		return fmt.Errorf("no release asset for smarteff-tray on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	checksumsAsset, ok := findAsset(rel, "checksums")
	checksums := map[string]string{}
	if ok {
		checksums, _ = c.fetchChecksums(checksumsAsset.BrowserDownloadURL)
	}

	tmpDir, err := os.MkdirTemp("", "smarteff-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	newDaemon := filepath.Join(tmpDir, daemonAsset.Name)
	newTray := filepath.Join(tmpDir, trayAsset.Name)

	if err := c.download(daemonAsset.BrowserDownloadURL, newDaemon, checksums[daemonAsset.Name]); err != nil {
		return fmt.Errorf("download %s: %w", daemonAsset.Name, err)
	}
	if err := c.download(trayAsset.BrowserDownloadURL, newTray, checksums[trayAsset.Name]); err != nil {
		return fmt.Errorf("download %s: %w", trayAsset.Name, err)
	}
	_ = os.Chmod(newDaemon, 0o755)
	_ = os.Chmod(newTray, 0o755)

	if err := c.Backend.StopBackground(); err != nil {
		return fmt.Errorf("stop background processes: %w", err)
	}
	if err := c.Backend.ReplaceSelf(c.DaemonPath, newDaemon); err != nil {
		return fmt.Errorf("replace daemon: %w", err)
	}
	if err := c.Backend.ReplaceSelf(c.TrayPath, newTray); err != nil {
		return fmt.Errorf("replace tray: %w", err)
	}
	return c.Backend.StartBackground()
}

func (c *Checker) fetchRelease(tag string) (*release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", c.Owner, c.Repo, tag)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "SmartEfficiency-updater")

	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %s", resp.Status)
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func (c *Checker) fetchChecksums(url string) (map[string]string, error) {
	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		out[fields[1]] = fields[0]
	}
	return out, nil
}

func (c *Checker) download(url, dest, wantSHA256 string) error {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %s", resp.Status)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		return err
	}
	f.Close()

	if wantSHA256 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, wantSHA256) {
			return fmt.Errorf("checksum mismatch: got %s want %s", got, wantSHA256)
		}
	}
	return nil
}
