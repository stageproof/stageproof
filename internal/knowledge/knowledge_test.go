package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeEntriesCiteHTTPS(t *testing.T) {
	if err := ValidateSources(); err != nil {
		t.Fatal(err)
	}
}

func TestEngineHasNoNames(t *testing.T) {
	deny := []string{"cnpg", "harbor", "longhorn", "izbirai", "terraform/modules"}
	roots := []string{"../.."}
	var hits []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel := filepath.ToSlash(path)
			if !strings.Contains(rel, "/cmd/") && !strings.Contains(rel, "/internal/") {
				return nil
			}
			if strings.Contains(rel, "/internal/knowledge/") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			low := strings.ToLower(string(raw))
			for _, word := range deny {
				if strings.Contains(low, word) {
					hits = append(hits, rel+": "+word)
				}
			}
			return nil
		})
	}
	if len(hits) > 0 {
		t.Fatalf("engine code names a vendor or a private path:\n%s", strings.Join(hits, "\n"))
	}
}
