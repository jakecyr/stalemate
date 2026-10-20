//go:build windows

package install

import (
	"errors"
	"os"
	"os/exec"
)

func detach(*exec.Cmd) error {
	return errors.New("background lifecycle is supported on macOS and Linux; use stalemate serve on Windows")
}
func terminate(*os.Process) error { return errors.New("use Ctrl+C to stop stalemate serve on Windows") }
func lock(string) (func(), error) { return func() {}, nil }
