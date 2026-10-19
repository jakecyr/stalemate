// Package domain contains provider- and persistence-independent knowledge records.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

func ID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
func JSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
func Now() int64 { return time.Now().UnixMilli() }

type Project struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	Module       string `json:"module"`
	CloudAllowed bool   `json:"cloud_allowed"`
}
type Evidence struct {
	ID       string `json:"id"`
	Project  string `json:"project"`
	Session  string `json:"session"`
	Kind     string `json:"kind"`
	Locator  string `json:"locator"`
	Revision string `json:"revision"`
	Text     string `json:"text"`
}
type Entity struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
}
type Relationship struct {
	ID         string   `json:"id"`
	From       string   `json:"from"`
	To         string   `json:"to"`
	Type       string   `json:"type"`
	Source     string   `json:"source"`
	Confidence float64  `json:"confidence"`
	Evidence   string   `json:"evidence"`
	Revision   string   `json:"revision"`
	APIs       []string `json:"apis"`
	Version    string   `json:"version"`
	Binding    string   `json:"binding"`
	ValidFrom  int64    `json:"valid_from"`
	ValidTo    int64    `json:"valid_to"`
}
type Claim struct {
	ID             string `json:"id"`
	Project        string `json:"project"`
	Entity         string `json:"entity"`
	Key            string `json:"key"`
	Text           string `json:"text"`
	Kind           string `json:"kind"`
	Status         string `json:"status"`
	Evidence       string `json:"evidence"`
	Previous       string `json:"previous"`
	Classification string `json:"classification"`
	Revision       string `json:"revision"`
	Created        int64  `json:"created"`
}
type Impact struct {
	ID           string   `json:"id"`
	Changed      string   `json:"changed_project"`
	Affected     string   `json:"affected_project"`
	ChangeKey    string   `json:"change_key"`
	Severity     string   `json:"severity"`
	Status       string   `json:"status"`
	Reason       string   `json:"reason"`
	Evidence     string   `json:"evidence"`
	Path         []string `json:"path"`
	Verification string   `json:"verification"`
	Created      int64    `json:"created"`
}
type Session struct {
	ID       string `json:"id"`
	Project  string `json:"project"`
	Source   string `json:"source"`
	Path     string `json:"path"`
	Cursor   int64  `json:"cursor"`
	Prefix   string `json:"prefix"`
	LastSeen int64  `json:"last_seen"`
	Status   string `json:"status"`
}
type Memory struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Session   string    `json:"session"`
	Text      string    `json:"text"`
	Embedding []float64 `json:"embedding,omitempty"`
	Model     string    `json:"model"`
	Created   int64     `json:"created"`
}
type Match struct {
	Memory Memory  `json:"memory"`
	Score  float64 `json:"score"`
}
type Job struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Payload   string `json:"payload"`
	Status    string `json:"status"`
	Attempts  int    `json:"attempts"`
	Available int64  `json:"available"`
	Lease     int64  `json:"lease"`
	Owner     string `json:"owner"`
	Error     string `json:"error"`
}
type Snapshot struct {
	Fingerprint  string            `json:"fingerprint"`
	Revision     string            `json:"revision"`
	Module       string            `json:"module"`
	PackageName  string            `json:"package_name"`
	APIs         map[string]string `json:"apis"`
	Bodies       map[string]string `json:"bodies"`
	SourceHash   string            `json:"source_hash"`
	Dependencies []Dependency      `json:"dependencies"`
	Files        []string          `json:"files"`
	Diff         string            `json:"diff"`
}
type Dependency struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Version  string   `json:"version"`
	APIs     []string `json:"apis"`
	Evidence string   `json:"evidence"`
	Kind     string   `json:"kind"`
}
type Change struct {
	API    string `json:"api"`
	Before string `json:"before"`
	After  string `json:"after"`
	Kind   string `json:"kind"`
}
type AutomationRun struct {
	ID              string   `json:"id"`
	Impact          string   `json:"impact"`
	Project         string   `json:"project"`
	Worktree        string   `json:"worktree"`
	Branch          string   `json:"branch"`
	Status          string   `json:"status"`
	Output          string   `json:"output"`
	PR              string   `json:"pr"`
	Created         int64    `json:"created"`
	VerificationEnv []string `json:"verification_env"`
}
