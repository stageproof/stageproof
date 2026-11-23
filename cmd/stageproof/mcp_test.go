package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMCPCommandRejectsAMissingRoot(t *testing.T) {
	var errb bytes.Buffer
	code := mcpCommand(context.Background(), []string{"--root", t.TempDir() + "/missing"}, &errb)
	if code != 2 || !strings.Contains(errb.String(), "--root") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	errb.Reset()
	if code := mcpCommand(context.Background(), []string{"-h"}, &errb); code != 0 {
		t.Fatalf("help exit %d stderr %q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "--root") || strings.Contains(errb.String(), "Usage of mcp") {
		t.Fatalf("help %q", errb.String())
	}
}

func TestMCPCommandExitsWhenStdinCloses(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code := mcpCommand(ctx, []string{"--root", t.TempDir()}, io.Discard)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if ctx.Err() != nil {
		t.Fatal("stdin close did not end the session")
	}
}
