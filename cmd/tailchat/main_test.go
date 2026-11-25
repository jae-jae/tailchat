package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("invalid handle")
}

func TestResilientLogWriterIgnoresMirrorError(t *testing.T) {
	var primary bytes.Buffer
	w := resilientLogWriter{
		primary: &primary,
		mirror:  failingWriter{},
	}

	msg := "tailchat: started on windowsgui\n"
	n, err := w.Write([]byte(msg))
	if err != nil {
		t.Fatalf("Write returned unexpected error: %v", err)
	}
	if n != len(msg) {
		t.Fatalf("Write returned n=%d, want %d", n, len(msg))
	}
	if primary.String() != msg {
		t.Fatalf("primary buffer = %q, want %q", primary.String(), msg)
	}
}

func TestSetupPersistentLogWritesStartupAndShutdown(t *testing.T) {
	dir := t.TempDir()
	closeLog := setupPersistentLog(dir)
	closeLog()

	data, err := os.ReadFile(filepath.Join(dir, "tailchat.log"))
	if err != nil {
		t.Fatalf("read tailchat.log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "tailchat: started") || !strings.Contains(content, "tailchat: shutting down") {
		t.Fatalf("unexpected tailchat.log content: %q", content)
	}
}
