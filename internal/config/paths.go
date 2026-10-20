package config

import (
	"os"
	"path/filepath"
)

// UserPath is independent of the current project or working directory.
func UserPath() (string, error) {
	base := os.Getenv("STALEMATE_CONFIG_DIR")
	if base == "" {
		var err error
		base, err = os.UserConfigDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(base, "stalemate")
	}
	return filepath.Abs(filepath.Join(base, "config.json"))
}
func EnvPath(path string) string { return filepath.Join(filepath.Dir(path), "environment") }

func Discover(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if path := os.Getenv("STALEMATE_CONFIG"); path != "" {
		return path, nil
	}
	if _, err := os.Stat("stalemate.json"); err == nil {
		return "stalemate.json", nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	path, err := UserPath()
	if err != nil {
		return "", err
	}
	if _, err = os.Stat(path); err == nil {
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return "", nil
}
