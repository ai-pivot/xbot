package plugin

import (
	"bufio"
	"bytes"
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

// jsonLineReader reads newline-delimited JSON objects.
//
// Deliberately NOT bufio.Scanner: Scanner enforces a maximum token size and
// aborts with "bufio.Scanner: token too long" once a single line exceeds it.
// Plugin responses are arbitrary — a big git diff legitimately produces a
// multi-megabyte line — and that abort killed the plugin's stdout, surfacing to
// users as a hard "plugin stdout closed" crash. bufio.Reader has no line limit.
type jsonLineReader struct {
	r *bufio.Reader
}

func newJSONLineReader(r io.Reader) *jsonLineReader {
	return &jsonLineReader{r: bufio.NewReader(r)}
}

// readLine reads a single raw JSON line from stdout.
// The returned slice is freshly allocated by ReadBytes, so it is safe to retain.
func (j *jsonLineReader) readLine() ([]byte, error) {
	line, err := j.r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		if err == io.EOF {
			return nil, fmt.Errorf("plugin process exited (EOF)")
		}
		return nil, fmt.Errorf("read from plugin: %w", err)
	}
	line = bytes.TrimRight(line, "\r\n")
	if len(line) == 0 {
		if err == io.EOF {
			return nil, fmt.Errorf("plugin process exited (EOF)")
		}
		return nil, fmt.Errorf("empty line from plugin")
	}
	return line, nil
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

// ReadJSON reads a single JSON line from the reader.
//
// Uses bufio.Reader (not Scanner) so arbitrarily long lines work — see
// jsonLineReader for why a Scanner line limit is a crash, not a nicety.
func ReadJSON(r io.Reader, v any) error {
	line, err := bufio.NewReader(r).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		if err == io.EOF {
			return fmt.Errorf("EOF")
		}
		return err
	}
	return json.Unmarshal(bytes.TrimRight(line, "\r\n"), v)
}

// FormatJSON formats a value as pretty-printed JSON.
func FormatJSON(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("<error: %v>", err)
	}
	return string(data)
}
