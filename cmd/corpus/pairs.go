package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func runPairs(root, bin string, prOnly bool) ([]caseResult, error) {
	var file pairsFile
	if err := loadYAML(filepath.Join(root, "corpus", "pairs", "pairs.yaml"), &file); err != nil {
		return nil, fmt.Errorf("pairs.yaml: %w", err)
	}
	var lock lockFile
	if err := loadYAML(filepath.Join(root, "corpus", "pairs", "pairs.lock"), &lock); err != nil {
		return nil, fmt.Errorf("pairs.lock: %w", err)
	}
	sums := map[string]string{}
	for _, a := range lock.Archives {
		sums[a.Repo+"|"+a.Chart+"|"+a.Version] = a.SHA256
	}
	var labels labelsFile
	_ = loadYAML(filepath.Join(root, "corpus", "labels", "chart-pairs.yaml"), &labels)
	truth := map[string]string{}
	for _, l := range labels.Labels {
		truth[l.Case] = l.Truth
	}
	cache := chartCache()
	var out []caseResult
	for _, p := range file.Pairs {
		if prOnly && !p.PR {
			continue
		}
		start := time.Now()
		fromDir, err := ensureChart(cache, p.Repo, p.Chart, p.From, sums[p.Repo+"|"+p.Chart+"|"+p.From])
		if err != nil {
			return nil, fmt.Errorf("%s from: %w", p.ID, err)
		}
		toDir, err := ensureChart(cache, p.Repo, p.Chart, p.To, sums[p.Repo+"|"+p.Chart+"|"+p.To])
		if err != nil {
			return nil, fmt.Errorf("%s to: %w", p.ID, err)
		}
		verdict, _, err := checkCharts(bin, fromDir, toDir, "1.35", "")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.ID, err)
		}
		got := caseResult{
			ID:      p.ID,
			Kind:    "pair",
			Verdict: verdict,
			Labeled: truth[p.ID],
			Seconds: time.Since(start).Seconds(),
			OK:      true,
		}
		out = append(out, got)
		fmt.Printf("pair %s %s labeled=%s\n", p.ID, verdict, got.Labeled)
	}
	return out, nil
}

func chartCache() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "stageproof", "charts")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}
