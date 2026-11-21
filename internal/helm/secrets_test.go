package helm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretStringsIncludesSequenceValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "values.yaml")
	body := "tokens:\n  - super-secret-token\n  - ab\nname: kept-name-value\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := SecretStrings([]string{path})
	text := strings.Join(got, "\n")
	if !strings.Contains(text, "super-secret-token") {
		t.Fatalf("sequence value missing: %v", got)
	}
	if strings.Contains(text, "\nab") || strings.HasSuffix(text, "ab") || strings.Contains(text, "ab\n") {
		t.Fatalf("short token collected: %v", got)
	}
}

func TestSecretValuesFromSetsTakesTheValue(t *testing.T) {
	got := SecretValuesFromSets([]string{"config.logLevel=hi", "token=super-secret-token", "nested=val=more-secret"})
	text := strings.Join(got, "\n")
	if strings.Contains(text, "hi") || strings.Contains(text, "config.logLevel") {
		t.Fatalf("short or key leaked: %v", got)
	}
	if !strings.Contains(text, "super-secret-token") || !strings.Contains(text, "val=more-secret") {
		t.Fatalf("values: %v", got)
	}
}

func TestScrubRemovesTheSetValue(t *testing.T) {
	msg := Scrub("render failed: super-secret-token", SecretValuesFromSets([]string{"password=super-secret-token"}))
	if strings.Contains(msg, "super-secret-token") || !strings.Contains(msg, "[redacted]") {
		t.Fatalf("scrub: %s", msg)
	}
}
