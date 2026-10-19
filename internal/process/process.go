// Package process bounds subprocess lifetime, output and inherited credentials.
package process

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Limited struct {
	mu  sync.Mutex
	b   bytes.Buffer
	Max int
}

func (w *Limited) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	remaining := w.Max - w.b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		w.b.Write(p)
	}
	return n, nil
}
func (w *Limited) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.b.String() }
func Environment() []string {
	keys := []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "CODEX_HOME", "SYSTEMROOT"}
	out := []string{}
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return append(out, "GIT_TERMINAL_PROMPT=0")
}
func Run(ctx context.Context, dir string, argv []string, stdin io.Reader, stdout io.Writer) (string, error) {
	return RunEnv(ctx, dir, argv, stdin, stdout, nil)
}

// RunEnv adds service-owned tool settings without changing the parent process.
func RunEnv(ctx context.Context, dir string, argv []string, stdin io.Reader, stdout io.Writer, extra []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(Environment(), extra...)
	cmd.Stdin = stdin
	output := &Limited{Max: 1 << 20}
	cmd.Stderr = output
	if stdout != nil {
		cmd.Stdout = stdout
	} else {
		cmd.Stdout = output
	}
	cmd.WaitDelay = 3 * time.Second
	configure(cmd)
	err := cmd.Run()
	if err != nil {
		return output.String(), fmt.Errorf("%s failed: %w", argv[0], err)
	}
	return strings.ToValidUTF8(output.String(), ""), nil
}
