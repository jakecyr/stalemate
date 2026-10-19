// Package privacy applies the same disclosure boundary to storage and providers.
package privacy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:sk-[a-z0-9_-]{12,}|gh[pousr]_[a-z0-9_]{15,}|github_pat_[a-z0-9_]{15,}|xox[baprs]-[a-z0-9-]{10,}|(?:AKIA|ASIA)[A-Z0-9]{16}|npm_[a-z0-9]{16,}|AIza[a-z0-9_-]{20,}|Bearer\s+[a-z0-9._~+/-]{8,}=*)`),
	regexp.MustCompile(`(?im)(\b(?:api[_-]?key|access[_-]?token|authorization|password|secret|client[_-]?secret)\b\s*[=:]\s*)[^\s,;\n]+`),
	regexp.MustCompile(`-----BEGIN [^-]*PRIVATE KEY-----[\s\S]*?(?:-----END [^-]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`),
	regexp.MustCompile(`(https?://)[^\s/@:]+:[^\s/@]+@`),
}

type Filter struct {
	Exclude []string
	Secrets []string
	Home    string
}

func New(exclude []string) *Filter {
	f := &Filter{Exclude: exclude}
	f.Home, _ = os.UserHomeDir()
	for _, e := range os.Environ() {
		k, v, _ := strings.Cut(e, "=")
		k = strings.ToUpper(k)
		if len(v) >= 8 && (strings.Contains(k, "KEY") || strings.Contains(k, "TOKEN") || strings.Contains(k, "SECRET") || strings.Contains(k, "PASSWORD")) {
			f.Secrets = append(f.Secrets, v)
		}
	}
	return f
}
func (f *Filter) Text(s string) string {
	for _, secret := range f.Secrets {
		s = strings.ReplaceAll(s, secret, "[REDACTED]")
	}
	for i, p := range patterns {
		if i == 1 {
			s = p.ReplaceAllString(s, "${1}[REDACTED]")
		} else if i == 4 {
			s = p.ReplaceAllString(s, "${1}[REDACTED]@")
		} else {
			s = p.ReplaceAllString(s, "[REDACTED]")
		}
	}
	if f.Home != "" {
		s = strings.ReplaceAll(s, f.Home, "~")
	}
	return s
}
func (f *Filter) Allowed(path string) bool {
	path = filepath.ToSlash(path)
	for _, part := range strings.Split(path, "/") {
		if part == ".git" || part == "node_modules" || part == "vendor" || part == ".venv" || part == ".stalemate" {
			return false
		}
		for _, g := range f.Exclude {
			if ok, _ := filepath.Match(strings.ToLower(g), strings.ToLower(part)); ok {
				return false
			}
		}
	}
	return true
}

// JSON redacts string values without damaging JSON syntax or numeric vectors.
func (f *Filter) JSON(raw []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			var nested any
			if json.Unmarshal([]byte(x), &nested) == nil {
				b, _ := json.Marshal(walk(nested))
				return string(b)
			}
			return f.Text(x)
		case []any:
			for i, e := range x {
				x[i] = walk(e)
			}
			return x
		case map[string]any:
			for k, e := range x {
				lower := strings.ToLower(k)
				if lower == "api_key" || lower == "password" || lower == "authorization" || lower == "access_token" || lower == "client_secret" {
					x[k] = "[REDACTED]"
				} else {
					x[k] = walk(e)
				}
			}
			return x
		default:
			return v
		}
	}
	return json.Marshal(walk(value))
}
func Within(root, path string) bool {
	r, err := filepath.Rel(root, path)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}
