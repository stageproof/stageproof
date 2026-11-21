package config

import (
	"context"
	"strings"
	"time"

	"github.com/stageproof/stageproof/internal/verdict"
)

const (
	ruleConfigChanged = "config.changed_in_pr"
	ruleIgnoreExpired = "config.ignore_expired"
)

// CheckSettings is the config a command already chose.
// Changed is true when check-pr saw a different .stageproof.yaml on the branch.
type CheckSettings struct {
	Config  *Config
	Changed bool
}

type ctxKey struct{}

// WithCheck carries the config into stageproof check.
// check-pr sets it from the base ref so the working tree is not read.
func WithCheck(ctx context.Context, cfg *Config, changed bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKey{}, CheckSettings{Config: cfg, Changed: changed})
}

// FromContext reports the config a caller injected.
func FromContext(ctx context.Context) (CheckSettings, bool) {
	if ctx == nil {
		return CheckSettings{}, false
	}
	st, ok := ctx.Value(ctxKey{}).(CheckSettings)
	return st, ok
}

// Apply rewrites severities from policy and marks ignore matches.
// An expired entry does not match. It adds config.ignore_expired instead.
// Findings stay in the report.
func Apply(cfg *Config, rep *verdict.Report, now time.Time) {
	if cfg == nil || rep == nil {
		return
	}
	today := dateOnly(now.UTC())
	for i := range rep.Findings {
		f := &rep.Findings[i]
		f.Severity = cfg.level(f.RuleID, f.Severity)
		if ign, ok := cfg.match(f.RuleID, f.Resource, today); ok {
			f.Ignored = true
			f.IgnoreReason = strings.TrimSpace(ign.Reason)
			f.IgnoreExpires = strings.TrimSpace(ign.Expires)
		}
	}
	for _, ign := range cfg.Ignores {
		exp, err := time.Parse("2006-01-02", strings.TrimSpace(ign.Expires))
		if err != nil || !today.After(exp) {
			continue
		}
		rep.Findings = append(rep.Findings, cfg.expiredFinding(ign))
	}
}

// ApplyNew marks findings added after Apply.
// It does not add config.ignore_expired again.
func ApplyNew(cfg *Config, rep *verdict.Report, from int, now time.Time) {
	if cfg == nil || rep == nil || from >= len(rep.Findings) {
		return
	}
	if from < 0 {
		from = 0
	}
	today := dateOnly(now.UTC())
	for i := from; i < len(rep.Findings); i++ {
		f := &rep.Findings[i]
		f.Severity = cfg.level(f.RuleID, f.Severity)
		if ign, ok := cfg.match(f.RuleID, f.Resource, today); ok {
			f.Ignored = true
			f.IgnoreReason = strings.TrimSpace(ign.Reason)
			f.IgnoreExpires = strings.TrimSpace(ign.Expires)
		}
	}
}

// NoteConfigChanged appends config.changed_in_pr when it is not already there.
// A blocker that is not ignored moves the result to BLOCKED.
func NoteConfigChanged(cfg *Config, rep *verdict.Report, now time.Time) {
	if rep == nil || rep.HasRule(ruleConfigChanged) {
		return
	}
	f := verdict.Finding{
		RuleID:   ruleConfigChanged,
		Severity: verdict.SeverityWarning,
		File:     ".stageproof.yaml",
		Message:  "This pull request changes .stageproof.yaml. check-pr reads that file from the base ref, so this change does not affect ignores, policy, or the other settings.",
	}
	if cfg != nil {
		f.Severity = cfg.level(f.RuleID, f.Severity)
		if ign, ok := cfg.match(f.RuleID, "", dateOnly(now.UTC())); ok {
			f.Ignored = true
			f.IgnoreReason = strings.TrimSpace(ign.Reason)
			f.IgnoreExpires = strings.TrimSpace(ign.Expires)
		}
	}
	rep.Findings = append(rep.Findings, f)
	if f.Severity == verdict.SeverityBlocker && !f.Ignored {
		rep.Verdict = verdict.Blocked
		rep.ExitCode = verdict.Blocked.ExitCode()
	}
}

func (c *Config) expiredFinding(ign Ignore) verdict.Finding {
	resource := strings.TrimSpace(ign.Resource)
	if resource == "" {
		resource = "*"
	}
	f := verdict.Finding{
		RuleID:   ruleIgnoreExpired,
		Severity: verdict.SeverityWarning,
		Resource: resource,
		Message:  "Ignore of " + strings.TrimSpace(ign.Rule) + " for " + resource + " expired on " + strings.TrimSpace(ign.Expires) + ". Reason: " + oneLine(ign.Reason) + ". It no longer matches.",
	}
	f.Severity = c.level(f.RuleID, f.Severity)
	return f
}

func (c *Config) level(id string, fallback verdict.Severity) verdict.Severity {
	if c == nil || len(c.Policy) == 0 {
		return fallback
	}
	bestLen := -1
	best := ""
	for key, level := range c.Policy {
		if key == id || strings.HasPrefix(id, key+".") {
			if len(key) > bestLen {
				bestLen = len(key)
				best = level
			}
		}
	}
	switch best {
	case "block":
		return verdict.SeverityBlocker
	case "warn":
		return verdict.SeverityWarning
	case "info":
		return verdict.SeverityInfo
	default:
		return fallback
	}
}

func (c *Config) match(ruleID, resource string, today time.Time) (Ignore, bool) {
	if c == nil {
		return Ignore{}, false
	}
	for _, ign := range c.Ignores {
		if strings.TrimSpace(ign.Rule) != ruleID {
			continue
		}
		exp, err := time.Parse("2006-01-02", strings.TrimSpace(ign.Expires))
		if err != nil || today.After(exp) {
			continue
		}
		if !resourceMatch(ign.Resource, resource) {
			continue
		}
		return ign, true
	}
	return Ignore{}, false
}

func resourceMatch(pattern, resource string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return true
	}
	return globMatch(pattern, resource)
}

func globMatch(pattern, value string) bool {
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(value) {
		if pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == value[si]) {
			pi++
			si++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			star = pi
			mark = si
			pi++
			continue
		}
		if star >= 0 {
			pi = star + 1
			mark++
			si = mark
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

func dateOnly(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
