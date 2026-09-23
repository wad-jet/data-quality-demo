package dq

// Minimal Summary implementation to avoid pulling in full report package (which depends on producer/kgo).
// Provides fields and methods used by the watcher and its tests.

type Summary struct {
	Total     int
	DLQ       int
	DLQErrors int
}

func NewSummary() *Summary { return &Summary{} }

func (s *Summary) AddFinding(f interface{}) { // f is checks.Finding but we don't need its contents here
	s.Total++
}

func (s *Summary) AddDLQ()      { s.DLQ++ }
func (s *Summary) AddDLQError() { s.DLQErrors++ }
