package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const userAgent = "stageproof/corpus (https://github.com/stageproof/stageproof)"

func fetch(args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	n := fs.Int("charts", 50, "how many charts, each with one minor pair and one major pair")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	charts, err := selectCharts(*n + 15)
	if err != nil {
		return err
	}
	var pairs []pair
	var seen = map[string]bool{}
	var archives []archive
	prLeft := 20
	for _, ch := range charts {
		for _, kind := range []string{"minor", "major"} {
			from, to := ch.minorFrom, ch.minorTo
			if kind == "major" {
				from, to = ch.majorFrom, ch.majorTo
			}
			id := ch.repoName + "-" + ch.name + "-" + kind
			p := pair{ID: id, Repo: ch.repoURL, Chart: ch.name, From: from, To: to, Kind: kind}
			if prLeft > 0 {
				p.PR = true
				prLeft--
			}
			pairs = append(pairs, p)
			for _, v := range []string{from, to} {
				key := ch.repoURL + "|" + ch.name + "|" + v
				if seen[key] {
					continue
				}
				seen[key] = true
				archives = append(archives, archive{Repo: ch.repoURL, Chart: ch.name, Version: v})
			}
		}
	}
	failed, err := hashArchives(archives)
	if err != nil {
		return err
	}
	if len(failed) > 0 {
		var kept []pair
		for _, p := range pairs {
			if failed[p.Repo+"|"+p.Chart+"|"+p.From] || failed[p.Repo+"|"+p.Chart+"|"+p.To] {
				fmt.Fprintf(os.Stderr, "drop %s: chart archive was not downloaded\n", p.ID)
				continue
			}
			kept = append(kept, p)
		}
		pairs = kept
		var keptArch []archive
		for _, a := range archives {
			if failed[a.Repo+"|"+a.Chart+"|"+a.Version] || a.SHA256 == "" {
				continue
			}
			keptArch = append(keptArch, a)
		}
		archives = keptArch
	}
	both := map[string]int{}
	for _, p := range pairs {
		both[p.Repo+"|"+p.Chart]++
	}
	var trimmed []pair
	chartsKept := map[string]bool{}
	for _, p := range pairs {
		key := p.Repo + "|" + p.Chart
		if both[key] < 2 {
			continue
		}
		if !chartsKept[key] && len(chartsKept) >= *n {
			continue
		}
		chartsKept[key] = true
		p.PR = len(trimmed) < 20
		trimmed = append(trimmed, p)
	}
	pairs = trimmed
	need := map[string]bool{}
	for _, p := range pairs {
		need[p.Repo+"|"+p.Chart+"|"+p.From] = true
		need[p.Repo+"|"+p.Chart+"|"+p.To] = true
	}
	var used []archive
	for _, a := range archives {
		if need[a.Repo+"|"+a.Chart+"|"+a.Version] {
			used = append(used, a)
		}
	}
	archives = used
	if len(pairs) < *n*2 {
		return fmt.Errorf("only %d pairs downloaded, want %d", len(pairs), *n*2)
	}
	dir := filepath.Join(root, "corpus", "pairs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(dir, "pairs.yaml"), pairsFile{Pairs: pairs}); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(dir, "pairs.lock"), lockFile{Archives: archives}); err != nil {
		return err
	}
	labels := labelsFile{}
	for _, p := range pairs {
		truth := "clean"
		if p.Kind == "major" {
			truth = "breaks"
		}
		hub := strings.SplitN(p.ID, "-"+p.Chart+"-", 2)[0]
		labels.Labels = append(labels.Labels, label{
			Case:     p.ID,
			Truth:    truth,
			Evidence: "https://artifacthub.io/packages/helm/" + hub + "/" + p.Chart,
			By:       "version-boundary",
			Date:     nowDate(),
		})
	}
	labelDir := filepath.Join(root, "corpus", "labels")
	if err := os.MkdirAll(labelDir, 0o755); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(labelDir, "chart-pairs.yaml"), labels); err != nil {
		return err
	}
	fmt.Printf("wrote %d pairs\n", len(pairs))
	return nil
}

type chosen struct {
	repoName, repoURL, name string
	minorFrom, minorTo      string
	majorFrom, majorTo      string
}

func selectCharts(n int) ([]chosen, error) {
	var out []chosen
	for offset := 0; len(out) < n && offset < 400; offset += 50 {
		pkgs, err := searchCharts(offset)
		if err != nil {
			return nil, err
		}
		if len(pkgs) == 0 {
			break
		}
		for _, pkg := range pkgs {
			if len(out) >= n {
				break
			}
			detail, err := packageDetail(pkg.Repository.Name, pkg.Name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "skip %s/%s: %v\n", pkg.Repository.Name, pkg.Name, err)
				continue
			}
			minorFrom, minorTo, majorFrom, majorTo, ok := chooseVersions(detail.AvailableVersions)
			if !ok {
				continue
			}
			out = append(out, chosen{
				repoName:  pkg.Repository.Name,
				repoURL:   detail.Repository.URL,
				name:      pkg.Name,
				minorFrom: minorFrom, minorTo: minorTo,
				majorFrom: majorFrom, majorTo: majorTo,
			})
			fmt.Printf("chart %d %s/%s minor %s -> %s major %s -> %s\n", len(out), pkg.Repository.Name, pkg.Name, minorFrom, minorTo, majorFrom, majorTo)
		}
	}
	if len(out) < n {
		return nil, fmt.Errorf("found %d charts with a minor and a major pair, want %d", len(out), n)
	}
	return out, nil
}

type searchResp struct {
	Packages []struct {
		Name       string `json:"name"`
		Repository struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"repository"`
	} `json:"packages"`
}

type pkgDetail struct {
	Repository struct {
		URL string `json:"url"`
	} `json:"repository"`
	AvailableVersions []struct {
		Version    string `json:"version"`
		Prerelease bool   `json:"prerelease"`
	} `json:"available_versions"`
}

func searchCharts(offset int) ([]struct {
	Name       string `json:"name"`
	Repository struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"repository"`
}, error) {
	var resp searchResp
	url := fmt.Sprintf("https://artifacthub.io/api/v1/packages/search?kind=0&sort=stars&limit=50&offset=%d", offset)
	if err := getJSON(url, &resp); err != nil {
		return nil, err
	}
	return resp.Packages, nil
}

func packageDetail(repo, name string) (pkgDetail, error) {
	var d pkgDetail
	url := "https://artifacthub.io/api/v1/packages/helm/" + repo + "/" + name
	err := getJSON(url, &d)
	return d, err
}

func getJSON(url string, dest any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	time.Sleep(150 * time.Millisecond)
	return json.Unmarshal(body, dest)
}

type semver struct {
	major, minor, patch int
	raw                 string
}

func parseSemver(s string) (semver, bool) {
	raw := s
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return semver{}, false
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	pat := 0
	if len(parts) > 2 {
		// drop a build suffix such as 1.2.3+build
		num := strings.SplitN(parts[2], "+", 2)[0]
		num = strings.SplitN(num, "-", 2)[0]
		p, err := strconv.Atoi(num)
		if err != nil {
			return semver{}, false
		}
		pat = p
	}
	if err1 != nil || err2 != nil {
		return semver{}, false
	}
	return semver{maj, min, pat, raw}, true
}

func chooseVersions(in []struct {
	Version    string `json:"version"`
	Prerelease bool   `json:"prerelease"`
}) (minorFrom, minorTo, majorFrom, majorTo string, ok bool) {
	var vers []semver
	for _, v := range in {
		if v.Prerelease || strings.Contains(v.Version, "-") {
			continue
		}
		parsed, good := parseSemver(v.Version)
		if good {
			vers = append(vers, parsed)
		}
	}
	if len(vers) == 0 {
		return
	}
	sort.Slice(vers, func(i, j int) bool {
		a, b := vers[i], vers[j]
		if a.major != b.major {
			return a.major < b.major
		}
		if a.minor != b.minor {
			return a.minor < b.minor
		}
		return a.patch < b.patch
	})
	latest := vers[len(vers)-1]
	var prevMinor semver
	foundMinor := false
	for i := len(vers) - 1; i >= 0; i-- {
		v := vers[i]
		if v.major == latest.major && v.minor == latest.minor-1 {
			if !foundMinor || v.patch > prevMinor.patch {
				prevMinor = v
				foundMinor = true
			}
		}
	}
	var prevMajor semver
	foundMajor := false
	for i := len(vers) - 1; i >= 0; i-- {
		v := vers[i]
		if v.major == latest.major-1 {
			if !foundMajor || v.minor > prevMajor.minor || (v.minor == prevMajor.minor && v.patch > prevMajor.patch) {
				prevMajor = v
				foundMajor = true
			}
		}
	}
	if !foundMinor || !foundMajor {
		return
	}
	var latestMinor, latestMajor semver
	for _, v := range vers {
		if v.major == latest.major && v.minor == latest.minor && v.patch >= latestMinor.patch {
			latestMinor = v
		}
		if v.major == latest.major && (v.minor > latestMajor.minor || (v.minor == latestMajor.minor && v.patch >= latestMajor.patch)) {
			latestMajor = v
		}
	}
	return prevMinor.raw, latestMinor.raw, prevMajor.raw, latestMajor.raw, true
}

func hashArchives(items []archive) (map[string]bool, error) {
	cache := chartCache()
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := map[string]bool{}
	for i := range items {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			item := items[i]
			key := item.Repo + "|" + item.Chart + "|" + item.Version
			tgz := filepath.Join(cache, item.Chart+"-"+item.Version+".tgz")
			if _, err := os.Stat(tgz); err != nil {
				if err := helmPull(cache, item.Repo, item.Chart, item.Version); err != nil {
					fmt.Fprintf(os.Stderr, "pull %s %s: %v\n", item.Chart, item.Version, err)
					mu.Lock()
					failed[key] = true
					mu.Unlock()
					return
				}
			}
			sum, err := fileSHA(tgz)
			if err != nil {
				mu.Lock()
				failed[key] = true
				mu.Unlock()
				return
			}
			mu.Lock()
			items[i].SHA256 = sum
			mu.Unlock()
			fmt.Printf("locked %s %s\n", item.Chart, item.Version)
		}(i)
	}
	wg.Wait()
	return failed, nil
}

func writeYAML(path string, v any) error {
	raw, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}
