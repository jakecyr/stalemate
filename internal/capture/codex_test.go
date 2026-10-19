package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakecyr/stalemate/internal/domain"
)

func TestParserIgnoresSystemAndReasoningAndHandlesExec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.jsonl")
	lines := []string{
		domain.JSON(map[string]any{"type": "session_meta", "payload": map[string]string{"id": "one", "cwd": t.TempDir()}}),
		`{"type":"response_item","payload":{"type":"message","role":"system","content":[{"type":"input_text","text":"private system instructions"}]}}`,
		`{"type":"response_item","payload":{"type":"reasoning","text":"private reasoning"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Build auth"}]}}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"go test ./...","aggregated_output":"PASS","exit_code":0}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Tests passed"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":5}}`,
		`{"type":"new_event_from_future","payload":{"ignored":true}}`,
	}
	data := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	source := &Codex{}
	batch, err := source.Read(path, 0, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Completed || batch.Cursor != int64(len(data)) {
		t.Fatal(batch)
	}
	if strings.Contains(batch.Text, "private") {
		t.Fatal("captured hidden instructions/reasoning")
	}
	for _, v := range []string{"Build auth", "go test ./...", "PASS", "Tests passed"} {
		if !strings.Contains(batch.Text, v) {
			t.Fatal("missing", v)
		}
	}
}
func TestTruncatedTranscriptResetsCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	data := `{"type":"session_meta","payload":{"id":"s","cwd":"/tmp"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := (&Codex{}).Read(path, 99999, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if b.Cursor != int64(len(data)) {
		t.Fatal("did not reset truncated cursor")
	}
}
