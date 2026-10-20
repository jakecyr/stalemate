//go:build !windows

package install

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func detach(cmd *exec.Cmd) error    { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}; return nil }
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another service command is running")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
