// Package llm provides LLM client implementations.
//
// Importing this package registers a custom SSE decoder for
// "text/event-stream" content types. This is done via init() and
// affects the global ssestream decoder registry. Callers should be
// aware that importing llm modifies global SSE decoding behavior.
package llm

import (
	"bufio"
	"bytes"
	"io"

	"github.com/openai/openai-go/v3/packages/ssestream"
)

// init registers a filtered SSE decoder that skips comment lines (starting with ":")
// and empty data events. This reduces noise in SSE stream processing.
func init() {
	for _, contentType := range []string{
		"text/event-stream",
		"text/event-stream; charset=utf-8",
	} {
		ssestream.RegisterDecoder(contentType, func(rc io.ReadCloser) ssestream.Decoder {
			scn := bufio.NewScanner(rc)
			scn.Buffer(nil, bufio.MaxScanTokenSize<<9)
			return &sseEventFilterDecoder{rc: rc, scn: scn}
		})
	}
}

// sseEventFilterDecoder mirrors openai-go's eventStreamDecoder, but skips
// comment-only and empty-data SSE events before they reach JSON unmarshal.
type sseEventFilterDecoder struct {
	evt ssestream.Event
	rc  io.ReadCloser
	scn *bufio.Scanner
	err error
}

func (s *sseEventFilterDecoder) Next() bool {
	if s.err != nil {
		return false
	}

	event := ""
	data := bytes.NewBuffer(nil)

	for s.scn.Scan() {
		txt := s.scn.Bytes()

		// Blank line dispatches the current event. Per the SSE spec, events
		// with empty data are discarded instead of dispatched.
		if len(txt) == 0 {
			if len(bytes.TrimSpace(data.Bytes())) == 0 {
				event = ""
				data.Reset()
				continue
			}
			s.evt = ssestream.Event{
				Type: event,
				Data: data.Bytes(),
			}
			return true
		}

		name, value, _ := bytes.Cut(txt, []byte(":"))
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}

		switch string(name) {
		case "":
			// ": keep-alive" style comment.
			continue
		case "event":
			event = string(value)
		case "data":
			_, s.err = data.Write(value)
			if s.err != nil {
				return false
			}
			_, s.err = data.WriteRune('\n')
			if s.err != nil {
				return false
			}
		}
	}

	if s.scn.Err() != nil {
		s.err = s.scn.Err()
	}

	return false
}

func (s *sseEventFilterDecoder) Event() ssestream.Event {
	return s.evt
}

func (s *sseEventFilterDecoder) Close() error {
	return s.rc.Close()
}

func (s *sseEventFilterDecoder) Err() error {
	return s.err
}
