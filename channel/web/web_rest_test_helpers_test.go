package web

import (
	"encoding/json"
	"io"
	"testing"
)

type testAPIEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *apiError       `json:"error"`
}

func decodeAPIData(t *testing.T, reader io.Reader, dst any) testAPIEnvelope {
	t.Helper()
	var envelope testAPIEnvelope
	if err := json.NewDecoder(reader).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" && dst != nil {
		if err := json.Unmarshal(envelope.Data, dst); err != nil {
			t.Fatal(err)
		}
	}
	return envelope
}
