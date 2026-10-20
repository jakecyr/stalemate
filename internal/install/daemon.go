package install

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jakecyr/stalemate/internal/buildinfo"
	"github.com/jakecyr/stalemate/internal/config"
	"github.com/jakecyr/stalemate/internal/process"
)

type State struct {
	PID      int    `json:"pid"`
	Instance string `json:"instance"`
	URL      string `json:"url"`
	Running  bool   `json:"running"`
	Version  string `json:"version"`
	Log      string `json:"log"`
}

func statePath(c config.Config) string { return filepath.Join(c.DataDir, "service.json") }
func readState(c config.Config) (State, error) {
	var s State
	b, err := os.ReadFile(statePath(c))
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}
func URL(c config.Config) string {
	host, port, _ := net.SplitHostPort(c.HTTP.Listen)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
func status(ctx context.Context, c config.Config, s State) State {
	s.Running = false
	if s.URL == "" {
		s.URL = URL(c)
	}
	r, err := http.NewRequestWithContext(ctx, "GET", s.URL+"/api/status", nil)
	if err != nil {
		return s
	}
	if token := os.Getenv(c.HTTP.TokenEnv); token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
	resp, err := client.Do(r)
	if err != nil {
		return s
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return s
	}
	var v struct {
		PID      int    `json:"process_pid"`
		Instance string `json:"process_instance"`
		Version  string `json:"version"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v) == nil && v.PID == s.PID && v.Instance != "" && v.Instance == s.Instance {
		s.Running = true
		s.Version = v.Version
	}
	return s
}
func Status(ctx context.Context, c config.Config) (State, error) {
	s, err := readState(c)
	if os.IsNotExist(err) {
		return State{URL: URL(c)}, nil
	}
	if err != nil {
		return s, err
	}
	return status(ctx, c, s), nil
}
func save(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".stalemate-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		err = json.NewEncoder(f).Encode(v)
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func Start(ctx context.Context, c config.Config, configPath, envPath string) (State, error) {
	if err := os.MkdirAll(c.DataDir, 0700); err != nil {
		return State{}, err
	}
	unlock, err := lock(filepath.Join(c.DataDir, "service.lock"))
	if err != nil {
		return State{}, err
	}
	defer unlock()
	return start(ctx, c, configPath, envPath)
}
func start(ctx context.Context, c config.Config, configPath, envPath string) (State, error) {
	s, err := Status(ctx, c)
	if err != nil {
		return s, err
	}
	if s.Running {
		return s, nil
	}
	l, err := net.Listen("tcp", c.HTTP.Listen)
	if err != nil {
		return s, fmt.Errorf("dashboard port is already in use; choose another with setup --listen HOST:PORT")
	}
	l.Close()
	binary, err := os.Executable()
	if err != nil {
		return s, err
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return s, err
	}
	envPath, err = filepath.Abs(envPath)
	if err != nil {
		return s, err
	}
	if err = os.MkdirAll(filepath.Join(c.DataDir, "logs"), 0700); err != nil {
		return s, err
	}
	logPath := filepath.Join(c.DataDir, "logs", "service.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return s, err
	}
	defer log.Close()
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return s, err
	}
	instance := hex.EncodeToString(random)
	cmd := exec.Command(binary, "--config", configPath, "--env-file", envPath, "serve")
	cmd.Dir = filepath.Dir(configPath)
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Env = process.Environment()
	// A service child must see the same deployment overrides as its launcher.
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "STALEMATE_") && !strings.HasPrefix(entry, "STALEMATE_INSTANCE=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	for _, key := range []string{c.AI.APIKeyEnv, c.Database.DSNEnv, c.HTTP.TokenEnv, c.Notifications.SecretEnv, "SSH_AUTH_SOCK"} {
		if value, ok := os.LookupEnv(key); key != "" && ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.Env = append(cmd.Env, "STALEMATE_INSTANCE="+instance)
	if err = detach(cmd); err != nil {
		return s, err
	}
	if err = cmd.Start(); err != nil {
		return s, err
	}
	defer cmd.Process.Release()
	s = State{PID: cmd.Process.Pid, Instance: instance, URL: URL(c), Version: buildinfo.Version, Log: logPath}
	if err = save(statePath(c), s); err != nil {
		_ = cmd.Process.Kill()
		return s, err
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return s, ctx.Err()
		case <-deadline.C:
			_ = cmd.Process.Kill()
			return s, fmt.Errorf("service did not become healthy; inspect %s", logPath)
		case <-tick.C:
			if current := status(ctx, c, s); current.Running {
				return current, nil
			}
		}
	}
}
func Stop(ctx context.Context, c config.Config) error {
	if err := os.MkdirAll(c.DataDir, 0700); err != nil {
		return err
	}
	unlock, err := lock(filepath.Join(c.DataDir, "service.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	return stop(ctx, c)
}
func stop(ctx context.Context, c config.Config) error {
	s, err := Status(ctx, c)
	if err != nil {
		return err
	}
	if !s.Running {
		return nil
	}
	p, err := os.FindProcess(s.PID)
	if err != nil {
		return err
	}
	if err = terminate(p); err != nil {
		return err
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("service is still stopping; inspect the service log")
		case <-tick.C:
			if !status(ctx, c, s).Running {
				return os.Remove(statePath(c))
			}
		}
	}
}
func Restart(ctx context.Context, c config.Config, path, env string) (State, error) {
	if err := os.MkdirAll(c.DataDir, 0700); err != nil {
		return State{}, err
	}
	unlock, err := lock(filepath.Join(c.DataDir, "service.lock"))
	if err != nil {
		return State{}, err
	}
	defer unlock()
	if err = stop(ctx, c); err != nil {
		return State{}, err
	}
	return start(ctx, c, path, env)
}

// Preserve user-managed environment settings while updating a chosen secret.
func SetSecret(path, key, value string) error {
	if value == "" || key == "" || strings.ContainsAny(value, "\r\n\x00") || strings.ContainsAny(key, "= \t\r\n\x00") {
		return errors.New("invalid secret value or variable name")
	}
	var lines []string
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	found := false
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		name, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "export "), "=")
		if ok && strings.TrimSpace(name) == key {
			if !found {
				lines = append(lines, key+"="+value)
				found = true
			}
		} else if line != "" {
			lines = append(lines, line)
		}
	}
	if !found {
		lines = append(lines, key+"="+value)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".environment-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
