package reports

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// TimelineEvent records one scenario checkpoint, fault, observation, or
// assertion in chronological order.
type TimelineEvent struct {
	At       time.Time      `json:"at"`
	Scenario string         `json:"scenario"`
	Phase    string         `json:"phase"`
	Name     string         `json:"name"`
	Status   string         `json:"status"`
	Details  map[string]any `json:"details,omitempty"`
}

// Timeline appends JSONL evidence safely from concurrent observers.
type Timeline struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
}

// OpenTimeline opens an append-only event stream.
func OpenTimeline(path string) (*Timeline, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open timeline: %w", err)
	}
	return &Timeline{file: file, enc: json.NewEncoder(file)}, nil
}

// Record writes one event and supplies a UTC timestamp when omitted.
func (timeline *Timeline) Record(event TimelineEvent) error {
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	timeline.mu.Lock()
	defer timeline.mu.Unlock()
	if err := timeline.enc.Encode(event); err != nil {
		return fmt.Errorf("write timeline event: %w", err)
	}
	return timeline.file.Sync()
}

// Close flushes the event stream.
func (timeline *Timeline) Close() error {
	timeline.mu.Lock()
	defer timeline.mu.Unlock()
	return timeline.file.Close()
}
