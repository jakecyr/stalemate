// Package install provides setup, dependency diagnostics and local lifecycle.
package install

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/jakecyr/stalemate/internal/config"
)

type Check struct {
	Name      string `json:"name"`
	Required  bool   `json:"required"`
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
}

func Doctor(ctx context.Context, c config.Config) []Check {
	out := []Check{{Name: "SQLite", Available: true, Detail: "Embedded in the binary; no database installation required"}}
	for _, dep := range []struct {
		name     string
		required bool
		hint     string
	}{{"git", true, "Install Git with your system package manager"}, {"codex", false, "Optional: required for stalemate run and agent remediation"}, {"gh", c.Automation.PublishPR, "Optional: required only for draft PR publishing"}} {
		check := Check{Name: dep.name, Required: dep.required, Detail: dep.hint}
		if path, err := exec.LookPath(dep.name); err == nil {
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			b, err := exec.CommandContext(bounded, path, "--version").CombinedOutput()
			cancel()
			if err == nil {
				check.Available = true
				check.Detail = strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)[0]
			} else {
				check.Detail = "Installed executable could not run; check system tools and permissions"
			}
		}
		out = append(out, check)
	}
	return out
}
func Missing(checks []Check) bool {
	for _, c := range checks {
		if c.Required && !c.Available {
			return true
		}
	}
	return false
}
