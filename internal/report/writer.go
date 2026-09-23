package report

import (
	"encoding/json"
	"os"
	"sync"

	"dqdemo/internal/checks"
)

// FindingsWriter append'ит findings в JSONL-файл (по строке на finding).
type FindingsWriter struct {
	mu   sync.Mutex
	file *os.File
}

func NewFindingsWriter(path string) (*FindingsWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &FindingsWriter{file: f}, nil
}

func (w *FindingsWriter) Append(f checks.Finding) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = w.file.Write(append(b, '\n'))
	return err
}

func (w *FindingsWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}
