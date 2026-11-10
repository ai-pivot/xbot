package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadToolRejectsBinaryFile pins the producer-side fix for the production
// failure
//
//	append message batch item 1: ERROR: invalid byte sequence for encoding
//	"UTF8": 0x00 (SQLSTATE 22021)
//
// A binary file (ELF, PNG, tarball) carries NUL bytes. Read used to return the
// raw bytes as text: the model got megabytes of garbage AND PostgreSQL rejected
// the whole INSERT (SQLSTATE 22021), losing the turn. Read must refuse with an
// actionable error instead.
func TestReadToolRejectsBinaryFile(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "payload.bin")
	body := []byte("\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00binary payload\n")
	if err := os.WriteFile(bin, body, 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadTool{}
	ctx := &ToolContext{WorkingDir: dir}

	_, err := tool.Execute(ctx, `{"path":"payload.bin"}`)
	if err == nil {
		t.Fatal("binary file must be rejected, got nil error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "binary or non-UTF-8 file") {
		t.Errorf("error must say the file is binary, got: %s", msg)
	}
	if !strings.Contains(msg, "NUL byte at offset 7") {
		t.Errorf("error must name the NUL marker, got: %s", msg)
	}
	if !strings.Contains(msg, "payload.bin") {
		t.Errorf("error must name the file, got: %s", msg)
	}
	if !strings.Contains(msg, "Shell") {
		t.Errorf("error must point at the recovery tool, got: %s", msg)
	}
}

// TestReadToolRejectsBinaryWithoutNUL covers payloads that are not UTF-8 text
// but contain no NUL in the probe window (e.g. high-entropy binary or UTF-16).
func TestReadToolRejectsBinaryWithoutNUL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blob.dat")
	body := make([]byte, 512)
	for i := range body {
		body[i] = 0xff
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadTool{}
	ctx := &ToolContext{WorkingDir: dir}

	_, err := tool.Execute(ctx, `{"path":"blob.dat"}`)
	if err == nil {
		t.Fatal("non-UTF-8 payload must be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Errorf("error must explain the encoding mismatch, got: %s", err.Error())
	}
}

// TestReadToolAcceptsTextWithOneTornByte guards the probe threshold: a mostly
// textual file with a single damaged byte is still readable (the text pipeline
// repairs it). Rejecting it would break ordinary log reading.
func TestReadToolAcceptsTextWithOneTornByte(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	body := "2026-10-24 INFO 全仓 CREATE TABLE（非 test）\n" + "next line with a torn byte: \xef\xbc\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadTool{}
	ctx := &ToolContext{WorkingDir: dir}

	res, err := tool.Execute(ctx, `{"path":"log.txt"}`)
	if err != nil {
		t.Fatalf("mostly-text file must still be readable: %v", err)
	}
	if !strings.Contains(res.Summary, "全仓 CREATE TABLE") {
		t.Errorf("content lost: %s", res.Summary)
	}
}

// TestDetectBinaryProbeWindow documents the 8 KiB boundary: a NUL past the probe
// window is not used to reject the file (long text files must stay readable),
// while a NUL inside it is.
func TestDetectBinaryProbeWindow(t *testing.T) {
	if reason := detectBinary("text\x00"); !strings.Contains(reason, "NUL byte at offset 4") {
		t.Fatalf("NUL inside probe not detected: %q", reason)
	}

	late := strings.Repeat("a", binaryProbeBytes) + "\x00"
	if reason := detectBinary(late); reason != "" {
		t.Fatalf("NUL outside probe window must not reject the file, got %q", reason)
	}

	if reason := detectBinary("plain UTF-8 文本"); reason != "" {
		t.Fatalf("text misdetected as binary: %q", reason)
	}
	if reason := detectBinary(""); reason != "" {
		t.Fatalf("empty content misdetected as binary: %q", reason)
	}
}
