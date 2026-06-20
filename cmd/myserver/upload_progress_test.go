package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestUploadProgressReaderReportsProgressAndFinalSize(t *testing.T) {
	var out bytes.Buffer
	pr := newUploadProgressReader(strings.NewReader(strings.Repeat("x", 5)), "uploaded")
	pr.out = &out
	pr.every = 2

	got, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "xxxxx" {
		t.Fatalf("read = %q", got)
	}
	pr.Finish()

	text := out.String()
	if !strings.Contains(text, "uploaded 0.0MB") {
		t.Fatalf("progress output missing upload label/size: %q", text)
	}
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("final progress output should end with newline: %q", text)
	}
}
