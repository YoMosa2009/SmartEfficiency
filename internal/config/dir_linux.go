//go:build linux

package config

import (
	"os"
	"path/filepath"
)

func baseDir() (string, error) {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "smartefficiency"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "smartefficiency"), nil
}
