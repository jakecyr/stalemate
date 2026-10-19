// Package capture normalizes agent-specific transcripts into bounded evidence
// batches. Capture never assumes that an agent's completed turn completed a task.
package capture

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jakecyr/stalemate/internal/domain"
	"github.com/jakecyr/stalemate/internal/privacy"
)

type Header struct {
	ID     string
	CWD    string
	Prefix string
}
type Batch struct {
	Header    Header
	Cursor    int64
	Text      string
	Completed bool
	Changed   bool
	ModTime   time.Time
}
type Source interface {
	Name() string
	Discover(context.Context) ([]string, error)
	Header(string) (Header, error)
	Read(string, int64, int) (Batch, error)
}
type Codex struct{ Roots []string }

func (c *Codex) Name() string { return "codex" }
func (c *Codex) Discover(ctx context.Context) ([]string, error) {
	out := []string{}
	for _, root := range c.Roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			if len(out) > 50000 {
				return errors.New("capture discovery exceeds 50000 files")
			}
			if !d.IsDir() && d.Type().IsRegular() && strings.HasSuffix(path, ".jsonl") {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (c *Codex) Header(path string) (Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, err
	}
	defer f.Close()
	r := bufio.NewReader(io.LimitReader(f, 2<<20))
	line, err := r.ReadBytes('\n')
	if err != nil {
		return Header{}, fmt.Errorf("incomplete session metadata: %w", err)
	}
	var d struct {
		Type    string `json:"type"`
		Payload struct {
			ID        string `json:"id"`
			SessionID string `json:"session_id"`
			CWD       string `json:"cwd"`
		} `json:"payload"`
	}
	if err = json.Unmarshal(line, &d); err != nil {
		return Header{}, err
	}
	if d.Type != "session_meta" || d.Payload.CWD == "" {
		return Header{}, errors.New("not a supported Codex transcript")
	}
	if d.Payload.ID == "" {
		d.Payload.ID = d.Payload.SessionID
	}
	if d.Payload.ID == "" {
		return Header{}, errors.New("session metadata has no ID")
	}
	return Header{ID: d.Payload.ID, CWD: d.Payload.CWD, Prefix: domain.ID(string(line))}, nil
}
func (c *Codex) Read(path string, cursor int64, maxBytes int) (Batch, error) {
	h, err := c.Header(path)
	if err != nil {
		return Batch{}, err
	}
	b := Batch{Header: h, Cursor: cursor}
	f, err := os.Open(path)
	if err != nil {
		return b, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return b, err
	}
	b.ModTime = info.ModTime()
	if cursor < 0 || cursor > info.Size() {
		cursor = 0
		b.Cursor = 0
	}
	if _, err = f.Seek(cursor, io.SeekStart); err != nil {
		return b, err
	}
	const maxRead = 8 << 20
	r := bufio.NewReader(io.LimitReader(f, maxRead))
	consumed := 0
	var text strings.Builder
	for {
		line, e := r.ReadBytes('\n')
		if e != nil {
			if e == io.EOF {
				if len(line) == maxRead && consumed == 0 {
					return b, errors.New("transcript record exceeds 8 MiB")
				}
				break
			}
			return b, e
		}
		consumed += len(line)
		var d map[string]json.RawMessage
		if e = json.Unmarshal(line, &d); e != nil {
			return b, fmt.Errorf("invalid JSONL at byte %d", cursor+int64(consumed-len(line)))
		}
		chunk, complete := record(d)
		if complete {
			b.Completed = true
		}
		b.Cursor = cursor + int64(consumed)
		b.Changed = true
		if chunk != "" {
			remaining := maxBytes - text.Len()
			if remaining > 0 {
				if len(chunk) > remaining {
					chunk = strings.ToValidUTF8(chunk[:remaining], "") + " [truncated]"
				}
				text.WriteString(chunk)
				text.WriteByte('\n')
			}
		}
		if text.Len() >= maxBytes {
			break
		}
	}
	b.Text = text.String()
	return b, nil
}
func str(v json.RawMessage) string { var s string; _ = json.Unmarshal(v, &s); return s }
func record(d map[string]json.RawMessage) (string, bool) {
	t := str(d["type"])
	p := map[string]json.RawMessage{}
	_ = json.Unmarshal(d["payload"], &p)
	pt := str(p["type"])
	switch t {
	case "event_msg":
		switch pt {
		case "task_complete", "task_completed", "turn_complete":
			return "", true
		case "user_message", "agent_message":
			return "", false
		}
	case "response_item":
		switch pt {
		case "message":
			role := str(p["role"])
			if role != "user" && role != "assistant" {
				return "", false
			}
			var content []map[string]json.RawMessage
			_ = json.Unmarshal(p["content"], &content)
			parts := []string{}
			for _, v := range content {
				kind := str(v["type"])
				if kind == "input_text" || kind == "output_text" || kind == "text" {
					parts = append(parts, str(v["text"]))
				}
			}
			if len(parts) > 0 {
				return role + ": " + strings.Join(parts, "\n"), false
			}
		case "function_call", "custom_tool_call":
			return "tool " + str(p["name"]) + ": " + str(p["arguments"]) + str(p["input"]), false
		case "function_call_output", "custom_tool_call_output":
			return "tool result: " + str(p["output"]), false
		}
	case "compacted":
		return "agent recap (unverified): " + str(p["message"]), false
	// Codex exec --json events captured by the run wrapper.
	case "item.completed":
		var item struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Command  string `json:"command"`
			Output   string `json:"aggregated_output"`
			ExitCode *int   `json:"exit_code"`
		}
		_ = json.Unmarshal(d["item"], &item)
		switch item.Type {
		case "agent_message":
			return "assistant: " + item.Text, false
		case "command_execution":
			exit := "unknown"
			if item.ExitCode != nil {
				exit = fmt.Sprint(*item.ExitCode)
			}
			return fmt.Sprintf("tool: %s\nexit: %s\n%s", item.Command, exit, item.Output), false
		}
	case "turn.completed", "stalemate.completed":
		return "", true
	case "stalemate.objective":
		return "user: " + str(d["text"]), false
	}
	return "", false
}

// ProjectFor is exact and path-based. Similar names never establish identity.
func ProjectFor(cwd string, projects []domain.Project) (domain.Project, bool) {
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return domain.Project{}, false
	}
	best := -1
	var found domain.Project
	for _, p := range projects {
		if privacy.Within(p.Path, resolved) && len(p.Path) > best {
			best = len(p.Path)
			found = p
		}
	}
	return found, best >= 0
}
