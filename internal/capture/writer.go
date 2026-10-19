package capture

import (
	"bytes"
	"errors"
	"io"

	"github.com/jakecyr/stalemate/internal/privacy"
)

// RedactingWriter accepts arbitrarily split JSONL writes. Raw agent output is
// never written to the service spool, including when a subprocess fails midway.
type RedactingWriter struct {
	Out     io.Writer
	Filter  *privacy.Filter
	pending []byte
}

func (w *RedactingWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.pending = append(w.pending, p...)
	if len(w.pending) > 8<<20 {
		return 0, errors.New("agent output record exceeds 8 MiB")
	}
	for {
		idx := bytes.IndexByte(w.pending, '\n')
		if idx < 0 {
			break
		}
		line := w.pending[:idx]
		if len(bytes.TrimSpace(line)) > 0 {
			b, err := w.Filter.JSON(line)
			if err != nil {
				return 0, errors.New("agent emitted invalid JSONL")
			}
			if _, err = w.Out.Write(append(b, '\n')); err != nil {
				return 0, err
			}
		}
		w.pending = w.pending[idx+1:]
	}
	return n, nil
}
func (w *RedactingWriter) Close() error {
	if len(bytes.TrimSpace(w.pending)) != 0 {
		return errors.New("agent left an incomplete JSONL record")
	}
	return nil
}
