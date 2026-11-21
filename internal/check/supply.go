package check

import (
	"context"
	"fmt"

	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/supply"
	"github.com/stageproof/stageproof/internal/verdict"
)

// Supply asks the registries named by new or changed images whether the tag
// exists. --offline skips those requests and does not change the API server.
func Supply(ctx context.Context, in Input, st *State) {
	before := helm.ImageUses(st.BaseObjs)
	after := helm.ImageUses(st.HeadObjs)
	var platforms []string
	if in.Facts != nil {
		platforms = in.Facts.Platforms()
	}
	findings := supply.Check(ctx, before, after, supply.ChartDeprecated(in.HeadDir), supply.Options{
		Offline:   in.Offline,
		Platforms: platforms,
	})
	st.Report.Findings = append(st.Report.Findings, findings...)
	blockers := 0
	warnings := 0
	for _, f := range findings {
		switch f.Severity {
		case verdict.SeverityBlocker:
			blockers++
		case verdict.SeverityWarning:
			warnings++
		}
	}
	checked := 0
	if !in.Offline {
		checked = supply.ChangedCount(before, after)
	}
	status := "pass"
	switch {
	case blockers > 0:
		status = "fail"
	case warnings > 0:
		status = "warn"
	}
	st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
		Name:    "image tags",
		Status:  status,
		Summary: supplySummary(in.Offline, checked, findings, blockers),
	})
}

func supplySummary(offline bool, checked int, findings []verdict.Finding, blockers int) string {
	if offline {
		if hasRule(findings, "supply.chart.deprecated") {
			return "Registry checks skipped (--offline). The new chart is deprecated. --api-server is unchanged."
		}
		return "Registry checks skipped (--offline). --api-server is unchanged."
	}
	if blockers > 0 {
		return "a new or changed image tag was not found, or it lacks the required platform"
	}
	if checked == 0 {
		if hasRule(findings, "supply.chart.deprecated") {
			return "the new chart is deprecated"
		}
		return "no new or changed image"
	}
	if checked == 1 {
		return "1 new or changed image tag checked"
	}
	return fmt.Sprintf("%d new or changed image tags checked", checked)
}

func hasRule(findings []verdict.Finding, id string) bool {
	for _, f := range findings {
		if f.RuleID == id {
			return true
		}
	}
	return false
}
