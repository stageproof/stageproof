package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const replayUserAgent = "stageproof/corpus (https://github.com/stageproof/stageproof)"

var (
	replayRepoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,80}/[A-Za-z0-9][A-Za-z0-9_.-]{0,80}$`)
	replaySHAPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	replayVerPattern  = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}([.+-][0-9A-Za-z.]+)?$`)
)

type replayCase struct {
	Repo string `json:"repo"`
	SHA  string `json:"sha"`
	Path string `json:"path"`
	From string `json:"from"`
	To   string `json:"to"`
}

func loadReplayCases(path string) ([]replayCase, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []replayCase
	seen := map[string]bool{}
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) > 300 {
			return nil, fmt.Errorf("%s:%d: line is too long", path, i+1)
		}
		c, err := parseReplayLine(line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		key := c.Repo + " " + c.SHA + " " + c.Path
		if seen[key] {
			return nil, fmt.Errorf("%s:%d: duplicate case", path, i+1)
		}
		seen[key] = true
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no cases", path)
	}
	return out, nil
}

func parseReplayLine(line string) (replayCase, error) {
	dec := json.NewDecoder(strings.NewReader(line))
	dec.DisallowUnknownFields()
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil {
		return replayCase{}, err
	}
	if dec.More() {
		return replayCase{}, fmt.Errorf("extra JSON after the case")
	}
	want := []string{"repo", "sha", "path", "from", "to"}
	if len(fields) != len(want) {
		return replayCase{}, fmt.Errorf("a case has %d fields", len(fields))
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			return replayCase{}, fmt.Errorf("missing %s", key)
		}
	}
	var c replayCase
	if err := json.Unmarshal([]byte(line), &c); err != nil {
		return replayCase{}, err
	}
	if err := validateReplayCase(c); err != nil {
		return replayCase{}, err
	}
	return c, nil
}

func validateReplayCase(c replayCase) error {
	if !replayRepoPattern.MatchString(c.Repo) || strings.Contains(c.Repo, "..") {
		return fmt.Errorf("repo must be owner/name")
	}
	if !replaySHAPattern.MatchString(c.SHA) {
		return fmt.Errorf("sha must be 40 hex characters")
	}
	if !validReplayPath(c.Path) {
		return fmt.Errorf("path must be a yaml file inside the repository")
	}
	if !replayVerPattern.MatchString(c.From) || !replayVerPattern.MatchString(c.To) || c.From == c.To {
		return fmt.Errorf("from and to must be two different versions")
	}
	return nil
}

func validReplayPath(path string) bool {
	if path == "" || len(path) > 200 || strings.Contains(path, "\\") || strings.HasPrefix(path, "/") {
		return false
	}
	clean := path
	if strings.Contains(clean, "..") {
		return false
	}
	lower := strings.ToLower(clean)
	return strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")
}

func runReplay(root, bin string) ([]caseResult, summary, error) {
	cases, err := loadReplayCases(filepath.Join(root, "corpus", "replay", "cases.jsonl"))
	if err != nil {
		return nil, summary{}, err
	}
	if len(cases) < 50 {
		return nil, summary{}, fmt.Errorf("replay cases: %d, want at least 50", len(cases))
	}
	results := make([]caseResult, len(cases))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, c := range cases {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, c replayCase) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = runReplayCase(bin, c, i, fetchGitHub)
		}(i, c)
	}
	wg.Wait()
	sum := summarizeReplay(results)
	if sum.ReplayChecked == 0 {
		return results, sum, fmt.Errorf("replay produced no verdict")
	}
	return results, sum, nil
}

func runReplayCase(bin string, c replayCase, index int, fetch func(repo, sha string) (string, error)) caseResult {
	start := time.Now()
	res := caseResult{ID: fmt.Sprintf("replay-%03d", index+1), Kind: "replay"}
	dir, err := fetch(c.Repo, c.SHA)
	if err != nil {
		res.Seconds = time.Since(start).Seconds()
		res.Note = "fetch failed"
		return res
	}
	defer removeReplayDir(dir)
	verdict, rules, err := checkReplay(bin, dir, c.Path)
	res.Seconds = time.Since(start).Seconds()
	if err != nil {
		res.Note = "check failed"
		return res
	}
	res.Verdict = verdict
	res.Rules = rules
	res.OK = true
	return res
}

func checkReplay(bin, dir, rel string) (string, []string, error) {
	out := filepath.Join(dir, "replay-report.json")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "check", "--flux-file", rel, "--current-git", "HEAD^", "--api-server", "off", "--format", "json", "--out", out)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stdout = &stderr
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", nil, fmt.Errorf("timed out")
	}
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	raw, readErr := os.ReadFile(out)
	if readErr != nil {
		// Exit 2 with no file is the CLI saying the chart could not be read.
		// Historical chart versions are sometimes gone. That is NOT CHECKED.
		if code == 2 {
			return "NOT CHECKED", nil, nil
		}
		return "", nil, fmt.Errorf("check failed")
	}
	if err != nil && code != 1 && code != 2 {
		_ = os.Remove(out)
		return "", nil, fmt.Errorf("check failed")
	}
	var rep struct {
		Verdict  string `json:"verdict"`
		Findings []struct {
			RuleID string `json:"rule_id"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil || rep.Verdict == "" {
		return "", nil, fmt.Errorf("report did not parse")
	}
	seen := map[string]bool{}
	var rules []string
	for _, f := range rep.Findings {
		if f.RuleID == "" || seen[f.RuleID] {
			continue
		}
		seen[f.RuleID] = true
		rules = append(rules, f.RuleID)
	}
	sort.Strings(rules)
	_ = os.Remove(out)
	return rep.Verdict, rules, nil
}

func fetchGitHub(repo, sha string) (string, error) {
	if err := validateReplayCase(replayCase{Repo: repo, SHA: sha, Path: "keep.yaml", From: "1.0.0", To: "1.0.1"}); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "stageproof-replay-")
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		removeReplayDir(dir)
		return "", err
	}
	if _, err := gitIn(dir, "init", "-q", "-b", "main"); err != nil {
		return fail(err)
	}
	if _, err := gitIn(dir, "remote", "add", "origin", "https://github.com/"+repo+".git"); err != nil {
		return fail(err)
	}
	var fetched bool
	for attempt := 1; attempt <= 4; attempt++ {
		_, err = gitIn(dir, "-c", "safe.directory=*", "-c", "http.userAgent="+replayUserAgent, "fetch", "--depth=2", "origin", sha)
		if err == nil {
			fetched = true
			break
		}
		msg := err.Error()
		if !strings.Contains(msg, "429") && !strings.Contains(strings.ToLower(msg), "rate limit") {
			break
		}
		if attempt == 4 {
			break
		}
		time.Sleep(time.Duration(1<<attempt) * time.Second)
	}
	if !fetched {
		return fail(fmt.Errorf("fetch failed"))
	}
	if _, err := gitIn(dir, "checkout", "-q", "--detach", "FETCH_HEAD"); err != nil {
		return fail(err)
	}
	if _, err := gitIn(dir, "rev-parse", "--verify", "--quiet", "HEAD^"); err != nil {
		return fail(fmt.Errorf("parent missing"))
	}
	return dir, nil
}

func gitIn(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("git timed out")
	}
	if err != nil {
		text := strings.TrimSpace(string(out))
		if len(text) > 200 {
			text = text[:200]
		}
		return out, fmt.Errorf("%s", text)
	}
	return out, nil
}

func removeReplayDir(dir string) {
	if dir == "" || !filepath.IsAbs(dir) || !strings.HasPrefix(dir, os.TempDir()) {
		return
	}
	_ = os.RemoveAll(dir)
}

func summarizeReplay(in []caseResult) summary {
	var s summary
	var seconds []float64
	s.ReplayCases = len(in)
	for _, c := range in {
		seconds = append(seconds, c.Seconds)
		if !c.OK {
			s.ReplayErrors++
			continue
		}
		s.ReplayChecked++
		switch c.Verdict {
		case "PASSED":
			s.ReplayPassed++
		case "BLOCKED":
			s.ReplayBlocked++
		case "NOT CHECKED":
			s.ReplayNotChecked++
		case "PROVEN":
			s.ReplayProven++
		}
	}
	s.P50Seconds, s.P95Seconds = percentiles(seconds)
	return s
}

func formatReplaySummary(s summary) string {
	return fmt.Sprintf("replay %d checked / %d cases\nPASSED %d  BLOCKED %d  NOT CHECKED %d  PROVEN %d  errors %d\np50 %.2fs  p95 %.2fs\n",
		s.ReplayChecked, s.ReplayCases, s.ReplayPassed, s.ReplayBlocked, s.ReplayNotChecked, s.ReplayProven, s.ReplayErrors, s.P50Seconds, s.P95Seconds)
}
