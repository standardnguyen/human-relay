package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

const maxOutputBytes = 10 * 1024 // 10KB per field in audit log

type Event struct {
	Timestamp  time.Time              `json:"timestamp"`
	Event      string                 `json:"event"`
	RequestID  string                 `json:"request_id,omitempty"`
	Fields     map[string]interface{} `json:"fields,omitempty"`
}

type Logger struct {
	mu   sync.Mutex
	file *os.File
}

func NewLogger(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	return &Logger{file: f}, nil
}

func (l *Logger) Log(event string, requestID string, fields map[string]interface{}) {
	e := Event{
		Timestamp: time.Now().UTC(),
		Event:     event,
		RequestID: requestID,
		Fields:    fields,
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	data = append(data, '\n')

	l.mu.Lock()
	l.file.Write(data)
	l.mu.Unlock()
}

func (l *Logger) Close() error {
	return l.file.Close()
}

// Truncate returns s truncated to maxOutputBytes with a notice appended.
func Truncate(s string) string {
	if len(s) <= maxOutputBytes {
		return s
	}
	return s[:maxOutputBytes] + "\n[audit: truncated at 10KB]"
}
