//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// Windows autostart uses two Task Scheduler tasks (daemon + tray), each with
// the battery-safety/reliability settings this project spent real effort
// establishing the hard way in the original PowerShell version:
//   - DisallowStartIfOnBatteries / StopIfGoingOnBatteries = false (both
//     default to true on a fresh task and will silently make a background
//     monitor useless on battery if left alone)
//   - ExecutionTimeLimit = PT0S (unlimited - the 72h default would silently
//     kill a long-running daemon)
//   - Restart on failure, 3 attempts, 1 minute apart
//
// Named distinctly from the original PowerShell tasks
// (SmartEfficiencyMonitor/Tray/Watchdog) so installing this version can never
// collide with or overwrite the still-running PowerShell one on the same
// machine.
//
// Unlike the PowerShell version, there's no separate Watchdog task here: the
// daemon and tray binaries are built with -H=windowsgui, so they never
// allocate a console window in the first place (the whole "window keeps
// popping up" bug class the PowerShell version had to work around is
// structurally impossible here). Each process also watches the other's PID
// file and relaunches it directly if it's gone - see internal/watchdog -
// covering the same "must never silently stay down" requirement without a
// third scheduled task.

const (
	daemonTaskName = "SmartEfficiencyGoDaemon"
	trayTaskName   = "SmartEfficiencyGoTray"
)

func currentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	// user.Current() on Windows returns DOMAIN\User or COMPUTER\User; schtasks wants that form too.
	return u.Username, nil
}

func taskXML(name, execPath string) (string, error) {
	uname, err := currentUsername()
	if err != nil {
		return "", err
	}
	esc := func(s string) string {
		r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
		return r.Replace(s)
	}
	xml := `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>SmartEfficiency (Go) - ` + esc(name) + `</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + esc(uname) + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + esc(uname) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>3</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + esc(execPath) + `</Command>
    </Exec>
  </Actions>
</Task>`
	return xml, nil
}

func registerTask(name, execPath string) error {
	xml, err := taskXML(name, execPath)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "smarteff-task-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	// Task Scheduler expects UTF-16LE with BOM for the XML file.
	utf16 := utf16LEWithBOM(xml)
	if _, err := tmp.Write(utf16); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	cmd := exec.Command("schtasks.exe", "/Create", "/TN", name, "/XML", tmp.Name(), "/F")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if isAccessDenied(err) || strings.Contains(strings.ToLower(string(out)), "access is denied") {
			return fmt.Errorf("registering task %q requires administrator privileges - run the installer elevated: %s", name, strings.TrimSpace(string(out)))
		}
		return fmt.Errorf("schtasks /Create %q failed: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func winInstallTasks(daemonPath, trayPath string) error {
	if err := registerTask(daemonTaskName, daemonPath); err != nil {
		return err
	}
	if err := registerTask(trayTaskName, trayPath); err != nil {
		return err
	}
	return winStartTasks()
}

func winUninstallTasks() error {
	var firstErr error
	for _, n := range []string{daemonTaskName, trayTaskName} {
		out, err := exec.Command("schtasks.exe", "/Delete", "/TN", n, "/F").CombinedOutput()
		if err != nil && firstErr == nil && !strings.Contains(strings.ToLower(string(out)), "cannot find") {
			firstErr = fmt.Errorf("schtasks /Delete %q: %v: %s", n, err, strings.TrimSpace(string(out)))
		}
	}
	return firstErr
}

func winStartTasks() error {
	for _, n := range []string{daemonTaskName, trayTaskName} {
		out, err := exec.Command("schtasks.exe", "/Run", "/TN", n).CombinedOutput()
		if err != nil {
			return fmt.Errorf("schtasks /Run %q: %v: %s", n, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func winStopTasks() error {
	for _, n := range []string{daemonTaskName, trayTaskName} {
		// Best-effort: End /F on a task that's not running just returns a
		// harmless error, not worth failing the whole stop over.
		_ = exec.Command("schtasks.exe", "/End", "/TN", n).Run()
	}
	return nil
}

// utf16LEWithBOM encodes s as UTF-16LE with a leading byte-order-mark, the
// encoding Task Scheduler requires for XML task definitions passed via file.
func utf16LEWithBOM(s string) []byte {
	runes := []rune(s)
	buf := make([]byte, 0, len(runes)*2+2)
	buf = append(buf, 0xFF, 0xFE) // BOM
	for _, r := range runes {
		if r > 0xFFFF {
			r = '?' // XML content here is all ASCII/paths; no real surrogate-pair risk
		}
		buf = append(buf, byte(r), byte(r>>8))
	}
	return buf
}
