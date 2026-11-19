package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/stageproof/stageproof/internal/verdict"
)

// LoadSavedLive reads a rehearsal that was run somewhere CI cannot run kind.
// dir may be a report.json file, or a directory containing report.json or
// <headSHA>/report.json. A saved verdict is ignored when its head_git is set
// and is a different commit.
func LoadSavedLive(dir, headSHA string) ([]*verdict.Report, error) {
	dir = strings.TrimSpace(dir)
	headSHA = strings.TrimSpace(headSHA)
	if dir == "" || headSHA == "" {
		return nil, nil
	}
	paths := []string{dir}
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		paths = []string{
			filepath.Join(dir, headSHA, "report.json"),
			filepath.Join(dir, "report.json"),
		}
	}
	var out []*verdict.Report
	seen := map[string]bool{}
	for _, p := range paths {
		reps, git, err := readSaved(p)
		if err != nil || len(reps) == 0 {
			continue
		}
		if git != "" && git != headSHA {
			continue
		}
		for _, r := range reps {
			if r == nil || seen[r.HeadRef+"\x00"+string(r.Verdict)] {
				continue
			}
			seen[r.HeadRef+"\x00"+string(r.Verdict)] = true
			if r.HeadGit == "" {
				r.HeadGit = headSHA
			}
			out = append(out, r)
		}
	}
	return out, nil
}

func readSaved(path string) ([]*verdict.Report, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var combined Combined
	if json.Unmarshal(raw, &combined) == nil && len(combined.Reports) > 0 {
		return combined.Reports, strings.TrimSpace(combined.HeadGit), nil
	}
	var one verdict.Report
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, "", err
	}
	if one.Verdict == "" && one.ExitCode == 0 && len(one.Checks) == 0 {
		return nil, "", nil
	}
	return []*verdict.Report{&one}, strings.TrimSpace(one.HeadGit), nil
}
