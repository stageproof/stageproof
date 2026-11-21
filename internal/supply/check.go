// Package supply asks registries whether a new or changed image tag exists.
// It uses HEAD, and it fetches an image index only when that HEAD says the
// tag is a manifest list. It does not download layers.
package supply

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/verdict"
	"gopkg.in/yaml.v3"
)

const (
	// DefaultRequestTimeout is the limit for one registry request.
	DefaultRequestTimeout = 5 * time.Second
	// DefaultBudget is the limit for every registry request in one check.
	DefaultBudget   = 20 * time.Second
	maxPerHost      = 4
	defaultPlatform = "linux/amd64"
)

// Options controls one supply pass. Zero values are the production defaults.
type Options struct {
	Offline bool
	// Platforms are the OS and architecture pairs the image must contain,
	// written as "linux/amd64". Empty means linux/amd64.
	Platforms []string
	Timeout   time.Duration
	Budget    time.Duration
	// Insecure uses HTTP. Tests set it for a local registry.
	Insecure bool
	// RoundTripper replaces the HTTP client. Tests use it to count requests.
	RoundTripper http.RoundTripper
}

// Check reports missing tags, missing platforms, registry failures, repository
// moves, and a deprecated chart. Offline skips every registry request.
func Check(ctx context.Context, before, after []helm.ImageUse, deprecated bool, opt Options) []verdict.Finding {
	var out []verdict.Finding
	if deprecated {
		out = append(out, verdict.Finding{
			RuleID:   "supply.chart.deprecated",
			Severity: verdict.SeverityWarning,
			Message:  "The new chart's Chart.yaml sets deprecated to true.",
			Detail:   "The chart maintainer has marked this chart as deprecated.",
		})
	}
	if opt.Offline {
		return sortFindings(out)
	}
	groups, moves := changed(before, after)
	for _, mv := range moves {
		out = append(out, verdict.Finding{
			RuleID:   "supply.image.moved",
			Severity: verdict.SeverityInfo,
			Resource: mv.resource,
			Message:  "Image repository changed from `" + mv.from + "` to `" + mv.to + "`.",
			Detail:   "The registry or the repository is different. The tag check still asks the new repository.",
		})
	}
	if len(groups) == 0 {
		return sortFindings(out)
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	budget := opt.Budget
	if budget <= 0 {
		budget = DefaultBudget
	}
	bctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	probe := newProber(bctx, opt, timeout)
	results := probe.probeAll(groups)
	platforms := opt.Platforms
	if len(platforms) == 0 {
		platforms = []string{defaultPlatform}
	}
	for i, g := range groups {
		out = append(out, findingsFor(g, results[i], platforms)...)
	}
	return sortFindings(out)
}

// ChangedCount is the number of new or changed image references.
func ChangedCount(before, after []helm.ImageUse) int {
	groups, _ := changed(before, after)
	return len(groups)
}

// ChartDeprecated reports whether Chart.yaml in dir sets deprecated to true.
// A missing chart directory is not deprecated.
func ChartDeprecated(dir string) bool {
	if dir == "" {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	if err != nil {
		return false
	}
	var doc struct {
		Deprecated bool `yaml:"deprecated"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return false
	}
	return doc.Deprecated
}

type group struct {
	after     string
	resources []string
}

type move struct {
	from     string
	to       string
	resource string
}

func changed(before, after []helm.ImageUse) ([]group, []move) {
	old := map[string]helm.ImageUse{}
	for _, im := range before {
		old[im.Key] = im
	}
	order := append([]helm.ImageUse(nil), after...)
	sort.Slice(order, func(i, j int) bool { return order[i].Key < order[j].Key })
	byImage := map[string]*group{}
	var images []string
	seenMove := map[string]bool{}
	var moves []move
	for _, im := range order {
		prev, ok := old[im.Key]
		if ok && prev.Image == im.Image {
			continue
		}
		if strings.TrimSpace(im.Image) == "" {
			continue
		}
		g, seen := byImage[im.Image]
		if !seen {
			g = &group{after: im.Image}
			byImage[im.Image] = g
			images = append(images, im.Image)
		}
		if im.Resource != "" {
			g.resources = append(g.resources, im.Resource)
		}
		if !ok || prev.Image == "" {
			continue
		}
		from, fromOK := repository(prev.Image, false)
		to, toOK := repository(im.Image, false)
		if !fromOK || !toOK || from == to {
			continue
		}
		key := from + "\n" + to
		if seenMove[key] {
			continue
		}
		seenMove[key] = true
		moves = append(moves, move{from: from, to: to, resource: im.Resource})
	}
	sort.Strings(images)
	out := make([]group, 0, len(images))
	for _, image := range images {
		g := *byImage[image]
		sort.Strings(g.resources)
		out = append(out, g)
	}
	sort.Slice(moves, func(i, j int) bool {
		if moves[i].from != moves[j].from {
			return moves[i].from < moves[j].from
		}
		return moves[i].to < moves[j].to
	})
	return out, moves
}

func findingsFor(g group, got outcome, platforms []string) []verdict.Finding {
	resource := g.after
	if len(g.resources) == 1 {
		resource = g.resources[0]
	}
	switch got.kind {
	case kindMissing:
		return []verdict.Finding{{
			RuleID:   "supply.image.not_found",
			Severity: verdict.SeverityBlocker,
			Resource: resource,
			Message:  "Image `" + g.after + "` was not found.",
			Detail:   "The registry answered that this tag or digest is not there.",
		}}
	case kindPlatform:
		missing := missingPlatforms(got.platforms, platforms)
		if len(missing) == 0 {
			return nil
		}
		return []verdict.Finding{{
			RuleID:   "supply.image.platform_missing",
			Severity: verdict.SeverityBlocker,
			Resource: resource,
			Message:  "Image `" + g.after + "` has no manifest for " + strings.Join(missing, ", ") + ".",
			Detail:   "The tag is a manifest list, and the required platform is not in it.",
		}}
	case kindUnverifiable:
		return []verdict.Finding{{
			RuleID:   "supply.image.unverifiable",
			Severity: verdict.SeverityInfo,
			Resource: resource,
			Message:  "Image `" + g.after + "` could not be checked. The registry refused the request.",
			Detail:   "A private image and a missing image can both look like this. This is not proof that the tag is missing.",
		}}
	case kindUnreachable:
		return []verdict.Finding{{
			RuleID:   "supply.registry_unreachable",
			Severity: verdict.SeverityInfo,
			Resource: resource,
			Message:  "Image `" + g.after + "` could not be checked. The registry did not answer.",
			Detail:   "The request timed out, or the registry returned a rate limit or a server error. This is not proof that the tag is missing.",
		}}
	default:
		return nil
	}
}

func missingPlatforms(have, want []string) []string {
	got := map[string]bool{}
	for _, p := range have {
		got[p] = true
	}
	var missing []string
	seen := map[string]bool{}
	for _, p := range want {
		if p == "" || got[p] || seen[p] {
			continue
		}
		seen[p] = true
		missing = append(missing, p)
	}
	sort.Strings(missing)
	return missing
}

func sortFindings(in []verdict.Finding) []verdict.Finding {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Resource != in[j].Resource {
			return in[i].Resource < in[j].Resource
		}
		if in[i].RuleID != in[j].RuleID {
			return in[i].RuleID < in[j].RuleID
		}
		return in[i].Message < in[j].Message
	})
	return in
}
