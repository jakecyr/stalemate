package process

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProcessTimeoutBoundsDescendantLifetime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process group semantics")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, "", []string{"sh", "-c", "sleep 30 & wait"}, nil, nil)
	if err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("descendant held process open")
	}
}
func TestCredentialsAreNotInheritedAndOutputIsBounded(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("requires sh")
	}
	t.Setenv("OPENAI_API_KEY", "private-api-key")
	out, err := Run(context.Background(), "", []string{"sh", "-c", `printf '%s' "${OPENAI_API_KEY-unset}"`}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "unset" {
		t.Fatal("credential inherited")
	}
	w := &Limited{Max: 4}
	n, err := w.Write([]byte(strings.Repeat("x", 20)))
	if err != nil || n != 20 || w.String() != "xxxx" {
		t.Fatal("output cap failed")
	}
}
