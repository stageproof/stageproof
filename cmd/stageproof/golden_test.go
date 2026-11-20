package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stageproof/stageproof/internal/report"
	"github.com/stageproof/stageproof/internal/verdict"
)

// updateGoldens rewrites testdata/golden. Duration and timestamp are zeroed.
// go test ./cmd/stageproof -update
var updateGoldens = flag.Bool("update", false, "rewrite testdata/golden JSON reports")

func TestGoldenReports(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	root := moduleRoot(t)
	cases := []struct {
		name string
		run  func(t *testing.T) []byte
	}{
		{"clean-upgrade", func(t *testing.T) []byte {
			return canonReportFile(t, checkOut(t, root, []string{
				"--current-chart", "testdata/clean/base",
				"--new-chart", "testdata/clean/head",
			}))
		}},
		{"require-rehearsal", func(t *testing.T) []byte {
			return canonReportFile(t, checkOut(t, root, []string{
				"--current-chart", "testdata/clean/base",
				"--new-chart", "testdata/clean/head",
				"--require-rehearsal",
			}))
		}},
		{"dropped-key", func(t *testing.T) []byte {
			return canonReportFile(t, checkOut(t, root, []string{
				"--current-chart", "testdata/dropped/base",
				"--new-chart", "testdata/dropped/head",
				"-f", "testdata/dropped/values.yaml",
			}))
		}},
		{"removed-api", func(t *testing.T) []byte {
			return canonReportFile(t, checkOut(t, root, []string{
				"--current-chart", "testdata/removed-api/base",
				"--new-chart", "testdata/removed-api/head",
				"--current-k8s", "1.32",
			}))
		}},
		{"selector-changed", func(t *testing.T) []byte {
			return canonReportFile(t, checkOut(t, root, []string{
				"--current-chart", "testdata/selector/base",
				"--new-chart", "testdata/selector/head",
			}))
		}},
		{"new-required", func(t *testing.T) []byte {
			return canonReportFile(t, checkOut(t, root, []string{
				"--current-chart", "testdata/required/base",
				"--new-chart", "testdata/required/head",
			}))
		}},
		{"image-changed", func(t *testing.T) []byte {
			return canonReportFile(t, checkOut(t, root, []string{
				"--current-chart", "testdata/image/base",
				"--new-chart", "testdata/image/head",
			}))
		}},
		{"pr-combined", func(t *testing.T) []byte {
			return canonCombinedFile(t, prCombinedOut(t))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.run(t)
			path := filepath.Join(root, "testdata", "golden", tc.name+".json")
			if *updateGoldens {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing golden %s (go test ./cmd/stageproof -update): %v", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("golden %s changed\n--- got ---\n%s\n--- want ---\n%s", tc.name, got, want)
			}
		})
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func checkOut(t *testing.T, root string, args []string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "report.json")
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	runCheck(append(args, "--out", out))
	return out
}

func canonReportFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rep verdict.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	rep.DurationMs = 0
	rep.Timestamp = time.Time{}
	rep.HeadGit = ""
	var buf bytes.Buffer
	if err := report.WriteJSON(&buf, &rep); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func canonCombinedFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var combined report.Combined
	if err := json.Unmarshal(raw, &combined); err != nil {
		t.Fatal(err)
	}
	combined.HeadGit = ""
	for _, rep := range combined.Reports {
		rep.DurationMs = 0
		rep.Timestamp = time.Time{}
		rep.HeadGit = ""
	}
	var buf bytes.Buffer
	if err := report.WriteCombinedJSON(&buf, &combined); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// prCombinedOut is the check-pr scenario in checkpr_test.go: two charts, one
// renamed values key, and the Kubernetes version from .stageproof.yaml.
func prCombinedOut(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	chart(t, "charts/api", "api", "1.0.0", "config.logLevel")
	chart(t, "charts/web", "web", "1.0.0", "config.logLevel")
	writeFile(t, "charts/api/values-prod.yaml", "replicas: 3\nconfig:\n  logLevel: warn\n")
	writeFile(t, ".stageproof.yaml", `currentKubernetes: "1.31"
charts:
  - name: api
    path: charts/api
    valuesFiles:
      - charts/api/values-prod.yaml
      - charts/api/values.yaml
`)
	gitAt(t, "2020-01-01T00:00:00Z", "init", "-q", "-b", "main")
	gitAt(t, "2020-01-01T00:00:00Z", "add", "-A")
	gitAt(t, "2020-01-01T00:00:00Z", "commit", "-q", "-m", "baseline")
	gitAt(t, "2020-01-02T00:00:00Z", "checkout", "-q", "-b", "change")
	chart(t, "charts/api", "api", "2.0.0", "logging.level")
	chart(t, "charts/web", "web", "1.0.1", "config.logLevel")
	gitAt(t, "2020-01-02T00:00:00Z", "add", "-A")
	gitAt(t, "2020-01-02T00:00:00Z", "commit", "-q", "-m", "change both charts")

	out := filepath.Join(t.TempDir(), "report.json")
	runCheckPR([]string{"--current-git", "main", "--out", out})
	return out
}

func gitAt(t *testing.T, when string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_AUTHOR_DATE="+when,
		"GIT_COMMITTER_DATE="+when,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
