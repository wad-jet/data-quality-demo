package producer

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type LedgerEntry struct {
	Seq     int64     `json:"seq"`
	OrderID string    `json:"order_id"`
	Ts      time.Time `json:"ts"`
	Defect  Defect    `json:"defect"`
}

// internal representation for writing, using time.Time for Ts formatting
type ledgerEntryInternal struct {
	Seq     int64     `json:"seq"`
	OrderID string    `json:"order_id"`
	Ts      time.Time `json:"ts"`
	Defect  Defect    `json:"defect"`
}

type Ledger struct {
	mu     sync.Mutex
	file   *os.File
	writer *bufio.Writer
	seq    int64
}

func NewLedger(path string) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Ledger{file: f, writer: bufio.NewWriter(f)}, nil
}

func (l *Ledger) Append(entry LedgerEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	entry.Seq = l.seq
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := l.writer.Write(append(b, '\n')); err != nil {
		return err
	}
	return l.writer.Flush()
}

func (l *Ledger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.writer.Flush(); err != nil {
		return err
	}
	return l.file.Close()
}
