//go:build windows

package config

import (
	"errors"
	"os"
	"path/filepath"
)

// Deliberately "SmartEfficiencyGo", not "SmartEfficiency": the latter is
// where the original PowerShell version (still the daemon actually running
// on the machine this was built on) keeps its own config.json/status.json/
// logs. Using the same folder would mean this version's config.Save()
// (which only knows its own Go struct fields) could silently truncate the
// PowerShell version's config.json the moment either one wrote to it. Never
// rename this back to plain "SmartEfficiency" while both versions can
// plausibly coexist on the same machine.
func baseDir() (string, error) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return "", errors.New("LOCALAPPDATA not set")
	}
	return filepath.Join(local, "SmartEfficiencyGo"), nil
}
