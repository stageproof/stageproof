package rules

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stageproof/stageproof/internal/knowledge"
	"github.com/stageproof/stageproof/internal/verdict"
)

// go test ./internal/rules -update
var updateDocs = flag.Bool("update", false, "rewrite the rule tables in web/docs.html")

func TestRegistryMatchesCode(t *testing.T) {
	got := ruleIDsInCode(t)
	ops, err := knowledge.Operators()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if op.Alias != "" {
			got[op.Alias] = true
		}
	}
	registered := map[string]bool{}
	var dup []string
	for _, rule := range Rules() {
		if rule.ID == "" || rule.Title == "" || rule.Explanation == "" || rule.Fix == "" {
			t.Errorf("rule %q is missing a title, explanation, or fix", rule.ID)
		}
		switch rule.Severity {
		case verdict.SeverityBlocker, verdict.SeverityWarning, verdict.SeverityInfo:
		default:
			t.Errorf("rule %q has severity %q", rule.ID, rule.Severity)
		}
		if _, ok := DocsTables()[rule.Group]; !ok {
			t.Errorf("rule %q has unknown group %q", rule.ID, rule.Group)
		}
		if registered[rule.ID] {
			dup = append(dup, rule.ID)
		}
		registered[rule.ID] = true
		text, ok := Explain(rule.ID)
		if !ok || !strings.Contains(text, rule.ID) || !strings.Contains(text, "What to do") {
			t.Errorf("explain %s = %q ok=%v", rule.ID, text, ok)
		}
		if strings.Contains(text, "%%") || strings.Contains(text, "[[") {
			t.Errorf("explain %s still has markup: %s", rule.ID, text)
		}
	}
	if len(dup) > 0 {
		t.Fatalf("duplicate rule ids: %s", strings.Join(dup, ", "))
	}
	var missing, extra []string
	for id := range got {
		if !registered[id] {
			missing = append(missing, id)
		}
	}
	for id := range registered {
		if !got[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("unregistered in code: %s\nregistered but not emitted: %s", strings.Join(missing, ", "), strings.Join(extra, ", "))
	}
	if _, ok := Explain("not.a.rule"); ok {
		t.Fatal("unknown id was explained")
	}
}

func TestDocsTablesMatchRegistry(t *testing.T) {
	path := filepath.Join(moduleRoot(t), "web", "docs.html")
	if *updateDocs {
		if err := SyncDocs(path); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for group, table := range DocsTables() {
		start := "<!-- rules:" + group + " -->"
		end := "<!-- /rules:" + group + " -->"
		if strings.Count(text, start) != 1 || strings.Count(text, end) != 1 {
			t.Fatalf("docs marker %s must appear once", group)
		}
		i := strings.Index(text, start)
		j := strings.Index(text, end)
		if j < i {
			t.Fatalf("docs marker %s is out of order", group)
		}
		got := strings.Trim(text[i+len(start):j], "\n")
		if got != table {
			t.Fatalf("docs table %s drifted from the registry (go test ./internal/rules -update)", group)
		}
		for _, rule := range Rules() {
			if rule.Group == group && !strings.Contains(got, "<code>"+rule.ID+"</code>") {
				t.Errorf("docs table %s does not document %s", group, rule.ID)
			}
		}
	}
}

func ruleIDsInCode(t *testing.T) map[string]bool {
	t.Helper()
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`RuleID:\s*"([^"]+)"`),
		regexp.MustCompile(`(?m)^[ \t]*[A-Za-z0-9_]*[Rr]ule[A-Za-z0-9_]*\s*=\s*"([^"]+)"`),
		regexp.MustCompile(`deleteFinding\(\s*"([^"]+)"`),
		regexp.MustCompile(`\brule:\s*"([^"]+)"`),
		regexp.MustCompile(`boundChanges\((?:[^,]+,){2}\s*"([^"]+)"`),
	}
	root := moduleRoot(t)
	out := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
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
		if strings.Contains(rel, "/internal/rules/") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, re := range patterns {
			for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
				out[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
