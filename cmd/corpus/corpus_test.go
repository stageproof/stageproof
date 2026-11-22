package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChooseVersionsNeedsMinorAndMajor(t *testing.T) {
	in := []struct {
		Version    string `json:"version"`
		Prerelease bool   `json:"prerelease"`
	}{
		{Version: "2.1.4"},
		{Version: "2.0.9"},
		{Version: "1.8.2"},
		{Version: "2.1.5-rc.1", Prerelease: true},
	}
	minorFrom, minorTo, majorFrom, majorTo, ok := chooseVersions(in)
	if !ok || minorFrom != "2.0.9" || minorTo != "2.1.4" || majorFrom != "1.8.2" || majorTo != "2.1.4" {
		t.Fatalf("got %s %s %s %s ok=%v", minorFrom, minorTo, majorFrom, majorTo, ok)
	}
}

func TestCorpusFilesParse(t *testing.T) {
	root := filepath.Join("..", "..")
	var pairs pairsFile
	if err := loadYAML(filepath.Join(root, "corpus", "pairs", "pairs.yaml"), &pairs); err != nil {
		t.Fatal(err)
	}
	if len(pairs.Pairs) == 0 {
		t.Fatal("pairs.yaml has no pairs")
	}
	var labels labelsFile
	if err := loadYAML(filepath.Join(root, "corpus", "labels", "chart-pairs.yaml"), &labels); err != nil {
		t.Fatal(err)
	}
	if len(labels.Labels) == 0 {
		t.Fatal("chart-pairs.yaml has no labels")
	}
}

func TestCompareFailsAMissedIncident(t *testing.T) {
	dir := t.TempDir()
	results := filepath.Join(dir, "results.json")
	base := filepath.Join(dir, "baseline.json")
	if err := os.WriteFile(results, []byte(`{"incidents":[{"id":"removed-api","status":"active","ok":false,"control_ok":true}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base, []byte(`{"blocked_clean":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := compare([]string{results, base}); err == nil {
		t.Fatal("a missed active incident must fail the compare")
	}
}

func TestRunRejectsUnknownAPIServerMode(t *testing.T) {
	if err := run([]string{"--set", "incidents", "--api-server", "bogus"}); err == nil {
		t.Fatal("invalid --api-server mode was accepted")
	}
}
