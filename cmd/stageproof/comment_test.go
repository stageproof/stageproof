package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommentCommandRejectsAMissingFile(t *testing.T) {
	var errb bytes.Buffer
	code := commentCommand(context.Background(), []string{"--file", filepath.Join(t.TempDir(), "missing.md")}, io.Discard, &errb, func(string) string { return "" })
	if code != 2 || !strings.Contains(errb.String(), "open ") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	errb.Reset()
	if code := commentCommand(context.Background(), []string{"-h"}, io.Discard, &errb, func(string) string { return "" }); code != 0 {
		t.Fatalf("help exit %d stderr %q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "--file") || !strings.Contains(errb.String(), "--best-effort") || strings.Contains(errb.String(), "Usage of comment") {
		t.Fatalf("help %q", errb.String())
	}
}
