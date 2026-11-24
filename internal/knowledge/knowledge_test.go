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

func TestServedAPIsLookup(t *testing.T) {
	for _, minor := range []string{"1.34", "1.35", "1.36", "1.37"} {
		apis, ok := ExactServedAPIs(minor)
		if !ok || len(apis) == 0 {
			t.Fatalf("missing served APIs for %s", minor)
		}
		has := map[string]bool{}
		for _, a := range apis {
			has[a] = true
		}
		for _, want := range []string{"batch/v1/CronJob", "policy/v1/PodDisruptionBudget", "apps/v1/Deployment"} {
			if !has[want] {
				t.Errorf("%s missing %s", minor, want)
			}
		}
	}
	if apis, ok := ExactServedAPIs("v1.35.0"); !ok || len(apis) == 0 {
		t.Fatal("v1.35.0 did not normalize to 1.35")
	}
	if _, ok := ExactServedAPIs("1.20"); ok {
		t.Fatal("1.20 should not have an exact entry")
	}
	if got := ServedAPIs("1.20"); len(got) != 0 {
		t.Fatalf("1.20 should not clamp to 1.34, got %d entries", len(got))
	}
	if got := ServedAPIs("1.33"); len(got) == 0 {
		t.Fatal("1.33 should fall back to nearest stored minor")
	}
	if got := ServedAPIs("1.38"); len(got) == 0 {
		t.Fatal("1.38 should fall back to nearest stored minor")
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
