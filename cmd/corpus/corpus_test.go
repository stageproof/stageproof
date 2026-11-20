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

func TestCompareFailsAMissedIncident(t *testing.T) {
	dir := t.TempDir()
	results := filepath.Join(dir, "results.json")
	base := filepath.Join(dir, "baseline.json")
	os.WriteFile(results, []byte(`{"incidents":[{"id":"removed-api","status":"active","ok":false,"control_ok":true}]}`), 0o644)
	os.WriteFile(base, []byte(`{"blocked_clean":[]}`), 0o644)
	if err := compare([]string{results, base}); err == nil {
		t.Fatal("a missed active incident must fail the compare")
	}
}
