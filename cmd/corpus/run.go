package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	set := fs.String("set", "incidents", "incidents, pairs, replay, or all")
	out := fs.String("out", "results.json", "where to write the JSON results")
	prOnly := fs.Bool("pr", false, "run only the fixed pull-request pair subset")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	bin, err := buildCLI(root)
	if err != nil {
		return err
	}
	var res resultsFile
	switch *set {
	case "incidents":
		res.Incidents, err = runIncidents(root, bin)
	case "pairs":
		res.Pairs, err = runPairs(root, bin, *prOnly)
	case "replay":
		fmt.Println("replay is not built yet. cases.jsonl is empty.")
		res.Summary = summarize(nil, nil)
		return writeResults(*out, res)
	case "all":
		res.Incidents, err = runIncidents(root, bin)
		if err != nil {
			return err
		}
		res.Pairs, err = runPairs(root, bin, false)
		fmt.Println("replay is not built yet.")
	default:
		return fmt.Errorf("unknown set %q", *set)
	}
	if err != nil {
		return err
	}
	res.Summary = summarize(res.Incidents, res.Pairs)
	printSummary(res.Summary)
	if err := writeResults(*out, res); err != nil {
		return err
	}
	if missedActive(res.Incidents) || failedControl(res.Incidents) {
		return fmt.Errorf("an active incident was missed or a control was blocked")
	}
	return nil
}

func buildCLI(root string) (string, error) {
	dir, err := os.MkdirTemp("", "stageproof-corpus-*")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "stageproof")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/stageproof")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("build stageproof: %w\n%s", err, out)
	}
	return bin, nil
}

func runIncidents(root, bin string) ([]caseResult, error) {
	dir := filepath.Join(root, "corpus", "incidents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	var out []caseResult
	for _, id := range ids {
		got, err := runIncident(root, bin, filepath.Join(dir, id))
		if err != nil {
			return nil, err
		}
		out = append(out, got)
		fmt.Printf("incident %s status=%s ok=%v control=%v verdict=%s\n", got.ID, got.Status, got.OK, got.ControlOK, got.Verdict)
	}
	return out, nil
}

func runIncident(root, bin, dir string) (caseResult, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "case.yaml"))
	if err != nil {
		return caseResult{}, err
	}
	var spec incidentCase
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		return caseResult{}, fmt.Errorf("%s: %w", dir, err)
	}
	res := caseResult{ID: spec.ID, Kind: "incident", Status: spec.Status, ControlOK: true}
	if spec.Status == "unreproduced" {
		res.OK = true
		res.Note = "not reproduced on a cluster: " + spec.Source
		return res, nil
	}
	start := time.Now()
	verdict, rules, err := checkCharts(bin, filepath.Join(dir, "before"), filepath.Join(dir, "after"), spec.K8s, valuesPath(dir, spec.Values))
	if err != nil {
		return caseResult{}, fmt.Errorf("%s: %w", spec.ID, err)
	}
	res.Verdict = verdict
	res.Rules = rules
	// expected-fail may miss. That miss does not fail the run. The control still must pass.
	res.OK = verdictMatches(verdict, rules, spec.Expect.Verdict, spec.Expect.Rules)
	cverdict, crules, err := checkCharts(bin, filepath.Join(dir, "before"), filepath.Join(dir, "control"), spec.K8s, valuesPath(dir, spec.Values))
	if err != nil {
		return caseResult{}, fmt.Errorf("%s control: %w", spec.ID, err)
	}
	res.ControlVerdict = cverdict
	res.ControlOK = verdictMatches(cverdict, crules, spec.ControlExpect.Verdict, spec.ControlExpect.Rules)
	res.Seconds = time.Since(start).Seconds()
	return res, nil
}

func valuesPath(dir, name string) string {
	if name == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

func verdictMatches(verdict string, rules []string, want string, wantRules []string) bool {
	if verdict != want {
		return false
	}
	have := map[string]bool{}
	for _, r := range rules {
		have[r] = true
	}
	for _, r := range wantRules {
		if !have[r] {
			return false
		}
	}
	return true
}

func checkCharts(bin, before, after, k8s, values string) (string, []string, error) {
	out := filepath.Join(os.TempDir(), "stageproof-corpus-"+filepath.Base(after)+".json")
	defer func() { _ = os.Remove(out) }()
	args := []string{"check", "--current-chart", before, "--new-chart", after, "--current-k8s", k8s, "--format", "json", "--out", out}
	if values != "" {
		args = append(args, "-f", values)
	}
	cmd := exec.Command(bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() != 1 && cmd.ProcessState.ExitCode() != 2 {
		return "", nil, fmt.Errorf("%w\n%s", err, stderr.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		return "", nil, fmt.Errorf("no report (%v): %s", err, stderr.String())
	}
	var rep struct {
		Verdict  string `json:"verdict"`
		Findings []struct {
			RuleID string `json:"rule_id"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		return "", nil, err
	}
	var rules []string
	for _, f := range rep.Findings {
		rules = append(rules, f.RuleID)
	}
	sort.Strings(rules)
	return rep.Verdict, rules, nil
}

func missedActive(in []caseResult) bool {
	for _, c := range in {
		if c.Status == "active" && !c.OK {
			return true
		}
	}
	return false
}

func failedControl(in []caseResult) bool {
	for _, c := range in {
		if c.Status == "unreproduced" {
			continue
		}
		if !c.ControlOK {
			return true
		}
	}
	return false
}

func printSummary(s summary) {
	fmt.Printf("incidents caught %d / %d active\n", s.IncidentsCaught, s.IncidentsActive)
	fmt.Printf("controls passed %d / %d\n", s.ControlsPassed, s.Controls)
	fmt.Printf("wrong blocks %d / %d labeled-clean\n", s.WrongBlocks, s.LabeledClean)
	fmt.Printf("p50 %.2fs  p95 %.2fs\n", s.P50Seconds, s.P95Seconds)
}

func summarize(incidents, pairs []caseResult) summary {
	var s summary
	var seconds []float64
	for _, c := range incidents {
		if c.Status == "unreproduced" {
			continue
		}
		s.Controls++
		if c.ControlOK {
			s.ControlsPassed++
		}
		if c.Status == "active" {
			s.IncidentsActive++
			if c.Verdict != "" && c.OK {
				s.IncidentsCaught++
			}
		}
		seconds = append(seconds, c.Seconds)
	}
	for _, c := range pairs {
		seconds = append(seconds, c.Seconds)
		if c.Labeled == "clean" {
			s.LabeledClean++
			if c.Verdict == "BLOCKED" {
				s.WrongBlocks++
			}
		}
	}
	s.P50Seconds, s.P95Seconds = percentiles(seconds)
	return s
}

func percentiles(v []float64) (float64, float64) {
	if len(v) == 0 {
		return 0, 0
	}
	sort.Float64s(v)
	at := func(p float64) float64 {
		i := int(p * float64(len(v)-1))
		return v[i]
	}
	return at(0.50), at(0.95)
}

func writeResults(path string, res resultsFile) error {
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

func loadYAML(path string, dest any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(raw, dest)
}
