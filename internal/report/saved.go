package report

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/stageproof/stageproof/internal/verdict"
)

// LoadSavedLive reads a rehearsal that was run somewhere CI cannot run kind.
// dir may be a report.json file, or a directory of report.json files one level
// down. accept is the set of git SHAs this merge request is allowed to trust:
// the head, and the commits on the merge request. A saved verdict whose
// head_git is outside that set is ignored, so an old report sitting on the
// default branch cannot fail a later merge request.
func LoadSavedLive(dir string, accept map[string]bool) ([]*verdict.Report, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" || len(accept) == 0 {
		return nil, nil
	}
	var paths []string
	st, err := os.Stat(dir)
	if err != nil {
		return nil, nil
	}
	if !st.IsDir() {
		paths = []string{dir}
	} else {
		paths = savedReportPaths(dir)
	}
	var out []*verdict.Report
	seen := map[string]bool{}
	for _, p := range paths {
		reps, git, err := readSaved(p)
		if err != nil || len(reps) == 0 || !savedApplies(git, p, accept) {
			continue
		}
		for _, r := range reps {
			if r == nil {
				continue
			}
			key := r.HeadRef + "\x00" + string(r.Verdict) + "\x00" + r.Cluster
			if seen[key] {
				continue
			}
			seen[key] = true
			if r.HeadGit == "" {
				r.HeadGit = git
			}
			out = append(out, r)
		}
	}
	return out, nil
}

func savedReportPaths(dir string) []string {
	var paths []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == "report.json" {
			paths = append(paths, path)
		}
		return nil
	})
	return paths
}

func savedApplies(git, path string, accept map[string]bool) bool {
	git = strings.TrimSpace(git)
	if git != "" {
		return accept[git]
	}
	for sha := range accept {
		if sha != "" && strings.Contains(path, sha) {
			return true
		}
	}
	return false
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
