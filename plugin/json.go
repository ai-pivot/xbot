package plugin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// jsonLineWriter writes JSON objects as newline-delimited lines.
type jsonLineWriter struct {
	w io.Writer
}

func (j *jsonLineWriter) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = j.w.Write(data)
	return err
}

// jsonLineReader reads newline-delimited JSON objects using bufio.Scanner.
// Supports lines up to 1MB.
const maxLineSize = 1 * 1024 * 1024 // 1MB

type jsonLineReader struct {
	scanner *bufio.Scanner
}

func newJSONLineReader(r io.Reader) *jsonLineReader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), maxLineSize)
	return &jsonLineReader{scanner: s}
}

// readLine reads a single raw JSON line from stdout.
// Returns the raw bytes (owned copy, safe to retain).
func (j *jsonLineReader) readLine() ([]byte, error) {
	if !j.scanner.Scan() {
		if err := j.scanner.Err(); err != nil {
			return nil, fmt.Errorf("read from plugin: %w", err)
		}
		return nil, fmt.Errorf("plugin process exited (EOF)")
	}
	line := j.scanner.Bytes()
	if len(line) == 0 {
		return nil, fmt.Errorf("empty line from plugin")
	}
	// Make a copy since scanner.Bytes() is only valid until next Scan().
	cp := make([]byte, len(line))
	copy(cp, line)
	return cp, nil
}

// WriteJSON marshals v as JSON followed by a newline and writes it to w.
func WriteJSON(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

// ReadJSON reads a single JSON line from the reader using bufio.Scanner.
func ReadJSON(r io.Reader, v any) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), maxLineSize)
	if !s.Scan() {
		if err := s.Err(); err != nil {
			return err
		}
		return fmt.Errorf("EOF")
	}
	return json.Unmarshal(s.Bytes(), v)
}

// FormatJSON formats a value as pretty-printed JSON.
func FormatJSON(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("<error: %v>", err)
	}
	return string(data)
}
