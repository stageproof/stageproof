package report

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stageproof/stageproof/internal/verdict"
)

func TestLoadSavedLiveMatchesMergeRequestSHA(t *testing.T) {
	dir := t.TempDir()
	sha := "c7e324b1df967b119ade3a8c56becab53c688ab2"
	parent := "6a38e547aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := os.MkdirAll(filepath.Join(dir, sha), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
	  "verdict": "NOT CHECKED",
	  "exit_code": 2,
	  "head_git": "` + sha + `",
	  "reports": [{
	    "verdict": "NOT CHECKED",
	    "exit_code": 2,
	    "base_ref": "demo@1.10.2",
	    "head_ref": "demo@1.11.3",
	    "cluster": "spmr50",
	    "checked": "files+live"
	  }]
	}`
	if err := os.WriteFile(filepath.Join(dir, sha, "report.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// The report commit is newer than the chart commit the rehearsal recorded.
	got, err := LoadSavedLive(dir, map[string]bool{parent: true, sha: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Verdict != verdict.NotChecked || got[0].Cluster != "spmr50" {
		t.Fatalf("got %+v", got)
	}
	other, err := LoadSavedLive(dir, map[string]bool{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("different sha should not load, got %+v", other)
	}
}
