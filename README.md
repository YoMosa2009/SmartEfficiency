# SmartEfficiency

A small background system that reduces battery/CPU/RAM waste from apps
sitting idle in the background: it puts unfocused, idle processes into the
OS's low-power scheduling mode, trims their memory, and backs off instantly
the moment you switch back to them. A tray icon shows what's happening live
and lets you pause it. It updates itself from this repo's GitHub Releases.

This is the Go rewrite of an earlier Windows-only PowerShell version, built
specifically to also run on Linux and macOS. **Read the "Platform status"
section below before trusting this on Linux or macOS** - only Windows has
actually been run and verified on real hardware while building this.

## Install

**Windows** (PowerShell):
```powershell
irm https://raw.githubusercontent.com/YoMosa2009/SmartEfficiency/main/install.ps1 | iex
```
Registering the background tasks needs Administrator - you'll get one UAC
prompt during install.

**Linux / macOS**:
```sh
curl -fsSL https://raw.githubusercontent.com/YoMosa2009/SmartEfficiency/main/install.sh | sh
```
No sudo needed - autostart is a per-user systemd/launchd registration.

Either way, this installs three small binaries and registers them to start
automatically at login:
- `smarteffd` - the background daemon that does the actual work
- `smarteff-tray` - the tray icon + local dashboard
- `smarteff-update` - a short-lived helper the other two use to self-update

To remove everything: `smarteffd -uninstall` (from wherever it was
installed - see "Files" below), then delete the install directory.

## Using it

Click the tray icon → **Open Dashboard**. That opens a small page (served
only on `127.0.0.1`, never reachable from the network) showing:
- whether monitoring is on or paused, with a toggle to pause/resume it
- battery %, power source, RAM usage
- how many processes are currently throttled, and how much RAM has been
  freed today
- the current version, and a "Check for updates" button

The tray menu itself also has a quick pause/resume toggle, for when opening
a browser tab is more than you want.

## How it decides what to throttle

Every poll cycle (default 15s):
- the process that currently owns the focused window is always left at
  normal priority
- anything else, once it's been unfocused for longer than the idle
  threshold (shorter on battery than plugged in), gets throttled
- if RAM usage crosses the high-pressure threshold, *everything* not
  currently focused gets throttled immediately, idle timer or not
- a short built-in list of core OS processes (explorer, systemd, launchd,
  etc.) is never touched, on top of whatever you add to
  `ExcludedProcessNames` in `config.json`

"Throttled" means, depending on platform: Windows' EcoQoS efficiency mode
(a genuine OS-level scheduling hint), or a lower `nice` value on Linux/macOS
(see platform notes below for what that actually gets you on each OS -
it's not the same guarantee everywhere).

## Platform status

Every mechanism this tool relies on - process throttling, memory trimming,
battery info, autostart registration - is a different OS API on each
platform; there's no shared substrate underneath. See
[`internal/platform/platform.go`](internal/platform/platform.go) for the
full interface and per-OS API mapping.

| | Windows | Linux | macOS |
|---|---|---|---|
| **Tested on real hardware** | ✅ Yes (Surface Laptop 3, Win 11) | ❌ No | ❌ No |
| Process throttling | EcoQoS (`SetProcessInformation`) - real OS-level hint | `nice` value - weaker, and *restoring* normal priority may be blocked by your distro's `RLIMIT_NICE` (unverified) | `nice` value, same caveat as Linux |
| Memory trimming | `EmptyWorkingSet` - works | Not implemented - no safe unprivileged equivalent exists (`process_madvise` needs `CAP_SYS_PTRACE`) | Not implemented, same reason |
| Foreground-app detection | `GetForegroundWindow` - reliable | `xdotool` (X11/XWayland only) - **no equivalent exists under native Wayland**; silently no-ops there | `System Events` via AppleScript - needs Accessibility permission granted on first run |
| Autostart | Task Scheduler | systemd user service | launchd agent |
| Tray icon build | Pure Go (no cgo) | Pure Go (D-Bus, no cgo) | Requires cgo - built natively on a macOS CI runner |

If you run this on Linux or macOS and something doesn't work, please open an
issue - the code was written carefully against each OS's documented
behavior, but "written correctly" and "actually verified" are different
claims, and only Windows got the second one so far.

**macOS releases are currently Apple Silicon (arm64) only.** An Intel
(amd64) build was attempted, and GitHub's Intel-Mac hosted
runner pool queued the build job with no machine ever assigned, twice in a
row. If you're on an Intel Mac, build from source
(see below) - the code itself doesn't care which Mac architecture it's on.

## Self-update

`smarteff-update` checks this repo's GitHub Releases
(`api.github.com/repos/YoMosa2009/SmartEfficiency/releases/latest`) - no
credentials needed, since the repo is public. The daemon runs this check
once a day by default (`AutoUpdateCheckIntervalHours` in `config.json`); the
dashboard also has a manual "Check for updates" button.

When an update is found, `smarteff-update` runs as its **own separate,
short-lived process** - not inside the daemon or tray - specifically because
a running process can't safely stop-and-replace its own executable file on
Windows (stopping its own scheduled task kills it before it can finish).
Being independent, it can safely: verify the new binaries' SHA-256 against
`checksums.txt` in the release, stop the daemon+tray, swap the files, and
start them again. If anything fails partway, whatever's already been left
running/stopped falls back to the OS's own restart-on-failure setting.

## Config

`config.json` lives next to the binaries (see "Files" below) and is
reloaded live - no restart needed after editing it.

| Field | Default | Meaning |
|---|---|---|
| `Enabled` | `true` | Master on/off switch (same as the dashboard toggle) |
| `PollIntervalSeconds` | `15` | How often the daemon re-evaluates everything |
| `IdleSecondsBase` | `120` | Idle time before throttling, plugged in |
| `IdleSecondsOnBattery` | `60` | Idle time before throttling, on battery |
| `RamPressureHighPct` | `85` | RAM% that triggers "throttle everything unfocused now" |
| `AutoUpdateEnabled` | `true` | Whether to check GitHub for updates at all |
| `AutoUpdateCheckIntervalHours` | `24` | How often to check |
| `ExcludedProcessNames` | `[]` | Process names to never throttle, in addition to the built-in critical list |

## Files

| OS | Directory |
|---|---|
| Windows | `%LOCALAPPDATA%\SmartEfficiencyGo\` |
| Linux | `$XDG_DATA_HOME/smartefficiency` (usually `~/.local/share/smartefficiency`) |
| macOS | `~/Library/Application Support/SmartEfficiency` |

Contains the three binaries, `config.json`, `status.json` (what the
dashboard reads), and `*.pid` files (used by the daemon/tray to watch each
other - see "Reliability" below).

On Windows specifically, the install directory is named
**`SmartEfficiencyGo`**, not `SmartEfficiency` - deliberately, so it can
never collide with an existing install of the original PowerShell version on
the same machine. Don't rename it back.

## Reliability

Each OS's own service manager restarts the daemon/tray if they crash (Task
Scheduler's `RestartOnFailure`, systemd's `Restart=always`, launchd's
`KeepAlive`). On top of that, the daemon and tray each watch the other's PID
file and relaunch it directly if it's gone - a lighter version of what the
original PowerShell project's separate "Watchdog" task did, made simpler
here because a Go process can just launch its sibling directly instead of
going through the OS scheduler.

Also worth knowing: `smarteffd` and `smarteff-tray` are built with
`-H=windowsgui` on Windows, meaning they never allocate a console window in
the first place - not "hidden," structurally absent. The original
PowerShell version had a real bug where `-WindowStyle Hidden` windows could
still flash visible under Windows 11's Windows Terminal delegation; that
whole class of bug doesn't exist here.

## Building from source

```sh
go build -ldflags "-X main.version=v0.0.0-local" ./cmd/smarteffd
go build -ldflags "-X main.version=v0.0.0-local" ./cmd/smarteff-tray
go build -ldflags "-X main.version=v0.0.0-local" ./cmd/smarteff-update
```
Add `-H=windowsgui` to the first two on Windows if you don't want a console
window while testing. See `.github/workflows/release.yml` for the exact
per-platform build matrix used for releases (Windows/Linux cross-compile
from one runner with `CGO_ENABLED=0`; macOS builds natively on a Mac runner
because its tray icon implementation needs cgo).

## Relationship to the original PowerShell version

This project started as a PowerShell + Task Scheduler system, Windows-only
by construction (Task Scheduler, `SetProcessInformation`, WinForms - none of
it exists elsewhere). This repo is a from-scratch rewrite, not a port, built
specifically to also run on Linux and macOS. The two can coexist on the same
Windows machine without conflict (see "Files" above) - nothing here modifies
or depends on the original.

## License

MIT - see [LICENSE](LICENSE).
