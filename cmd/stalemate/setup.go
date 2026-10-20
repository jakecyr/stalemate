package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jakecyr/stalemate/internal/config"
	"github.com/jakecyr/stalemate/internal/install"
	"golang.org/x/term"
)

func setup(ctx context.Context, path, envPath string, args []string, out io.Writer) error {
	f := flags("setup")
	noStart := f.Bool("no-start", false, "Prepare settings without starting a background service")
	openAI := f.Bool("openai", false, "Enable OpenAI and securely save its key")
	listen := f.String("listen", "", "Dashboard listen address (default 127.0.0.1:7433)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("setup accepts flags only")
	}
	if path == "" {
		path = os.Getenv("STALEMATE_CONFIG")
	}
	if path == "" {
		var err error
		path, err = config.UserPath()
		if err != nil {
			return err
		}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if envPath == "" {
		envPath = config.EnvPath(path)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(envPath), 0700); err != nil {
		return err
	}
	if e, err := os.OpenFile(envPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil {
		e.Close()
	} else if !os.IsExist(err) {
		return err
	}
	if err = config.LoadEnv(envPath); err != nil {
		return err
	}
	c := config.Default()
	existing := false
	if b, err := os.ReadFile(path); err == nil {
		existing = true
		if _, err = config.Load(path); err != nil {
			return err
		}
		if err = json.Unmarshal(b, &c); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if !existing {
		if dir := os.Getenv("STALEMATE_DATA_DIR"); dir != "" {
			c.DataDir, err = filepath.Abs(dir)
			if err != nil {
				return err
			}
		}
		if provider := os.Getenv("STALEMATE_AI_PROVIDER"); provider != "" {
			c.AI.Provider = provider
		}
	}
	if *listen != "" {
		c.HTTP.Listen = *listen
	}
	if *openAI {
		key := os.Getenv(c.AI.APIKeyEnv)
		if key == "" {
			key, err = readKey()
			if err != nil {
				return err
			}
		}
		if err = install.SetSecret(envPath, c.AI.APIKeyEnv, key); err != nil {
			return err
		}
		if err = os.Setenv(c.AI.APIKeyEnv, key); err != nil {
			return err
		}
		c.AI.Provider = "openai"
	}
	if err = c.Validate(); err != nil {
		return err
	}
	checks := install.Doctor(ctx, c)
	printChecks(out, checks)
	if install.Missing(checks) {
		return errors.New("Git is required; install it and run setup again (the installer handles supported package managers)")
	}
	// Persist settings, never a DSN resolved from a secret environment variable.
	if !existing || *listen != "" || *openAI {
		b, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if err = tmp.Chmod(0600); err == nil {
			_, err = tmp.Write(append(b, '\n'))
		}
		if e := tmp.Close(); err == nil {
			err = e
		}
		if err != nil {
			return err
		}
		if err = os.Rename(tmp.Name(), path); err != nil {
			return err
		}
	}
	fmt.Fprintln(out, "Settings:", path)
	resolved, err := config.Load(path)
	if err != nil {
		return err
	}
	if *noStart {
		fmt.Fprintln(out, "Ready. Start with: stalemate start")
		return nil
	}
	state, err := install.Restart(ctx, resolved, path, envPath)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Stalemate is running:", state.URL)
	fmt.Fprintln(out, "Register your projects in the dashboard. Logs:", state.Log)
	return nil
}
func printChecks(out io.Writer, checks []install.Check) {
	for _, c := range checks {
		mark := "ok"
		if !c.Available {
			mark = "optional"
			if c.Required {
				mark = "missing"
			}
		}
		fmt.Fprintf(out, "[%s] %s: %s\n", mark, c.Name, c.Detail)
	}
}
func readKey() (string, error) {
	f := os.Stdin
	var closeFile bool
	if !term.IsTerminal(int(f.Fd())) {
		var err error
		f, err = os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			return "", errors.New("set OPENAI_API_KEY or run stalemate setup --openai in a terminal")
		}
		closeFile = true
	}
	if closeFile {
		defer f.Close()
	}
	fmt.Fprint(os.Stderr, "OpenAI API key (hidden): ")
	b, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", errors.New("API key cannot be empty")
	}
	return key, nil
}
