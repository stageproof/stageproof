package check

import (
	"context"
	"fmt"
	"strings"

	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/k8sdiff"
	"github.com/stageproof/stageproof/internal/verdict"
)

// strictUnknown reports whether an unknown field rejects the upgrade.
// Helm 4 uses server-side apply for a new install. An upgrade follows the
// release's previous apply method, and a release Helm 3 created stays
// client-side. --server-side-apply forces the strict result.
// https://helm.sh/docs/overview/#server-side-apply
func strictUnknown(serverSide bool, helmMajor int, manifestMode, hasCurrentChart bool) bool {
	if serverSide {
		return true
	}
	if manifestMode || hasCurrentChart {
		return false
	}
	return helmMajor >= 4
}

// NoteChartAPI warns when the new chart's Chart.yaml apiVersion is v3.
// Charts v3 are experimental.
// https://helm.sh/docs/overview/#charts-v3
func NoteChartAPI(in Input, st *State) {
	if in.ManifestMode || in.HeadDir == "" {
		return
	}
	api, err := helm.ChartAPIVersion(in.HeadDir)
	if err != nil {
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
			Name:    "Helm 4",
			Status:  "warn",
			Summary: "Chart.yaml could not be read",
		})
		return
	}
	if api != "v3" {
		return
	}
	st.Report.Findings = append(st.Report.Findings, verdict.Finding{
		RuleID:   "helm4.chart_v3",
		Severity: verdict.SeverityWarning,
		File:     in.HeadDir,
		Message:  "The new chart's Chart.yaml apiVersion is v3.",
		Detail:   "Charts v3 are experimental. v2 charts work on Helm 3 and Helm 4. https://helm.sh/docs/overview/#charts-v3",
	})
	if in.HelmCompare != "" {
		return
	}
	st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
		Name:    "Helm 4",
		Status:  "warn",
		Summary: "the new chart uses chart apiVersion v3",
	})
}

// HelmCompare renders the chart with the helm on PATH and with --helm-compare.
// A field that changes between two renders of the same binary is ignored.
// https://helm.sh/docs/overview/#upgrading-to-helm-4
func HelmCompare(ctx context.Context, in Input, st *State) {
	if in.HelmCompare == "" || in.ManifestMode || st.Stopped {
		return
	}
	sides := []struct {
		label string
		opts  helm.RenderOptions
	}{
		{label: "the new chart", opts: headOptions(in, st)},
	}
	if in.BaseDir != "" {
		sides = append([]struct {
			label string
			opts  helm.RenderOptions
		}{{label: "the current chart", opts: baseOptions(in, st)}}, sides...)
	}
	var findings []verdict.Finding
	for _, side := range sides {
		secrets := helm.SecretStrings(side.opts.ValuesFiles)
		secrets = append(secrets, helm.SecretValuesFromSets(side.opts.SetValues)...)
		secrets = append(secrets, helm.SecretValuesFromSets(side.opts.SetStrings)...)
		diff := helm.CompareRenders(ctx, "helm", in.HelmCompare, side.opts, secrets)
		findings = append(findings, findingsFor(side.label, diff)...)
	}
	st.Report.Findings = append(st.Report.Findings, findings...)
	status := "pass"
	summary := "the two Helm binaries rendered the same objects"
	for _, f := range findings {
		if f.Severity == verdict.SeverityBlocker {
			status = "fail"
			summary = "the other Helm binary did not render the chart"
			break
		}
		if f.Severity == verdict.SeverityWarning && status == "pass" {
			status = "warn"
			summary = "the rendered objects differ"
		}
	}
	if status == "pass" && hasRule(st.Report.Findings, "helm4.chart_v3") {
		status = "warn"
		summary = "the new chart uses chart apiVersion v3, and the two Helm binaries rendered the same objects"
	}
	st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
		Name:    "Helm 4",
		Status:  status,
		Summary: summary,
	})
}

func headOptions(in Input, st *State) helm.RenderOptions {
	opts := in.renderOptions(in.HeadDir, in.HeadValues, in.HeadSet, in.HeadSetString, in.HeadSetFile, in.HeadRelease, in.HeadNamespace, in.HeadSkipCRDs)
	opts.SetStrings = append(opts.SetStrings, st.Placeholders...)
	return opts
}

func baseOptions(in Input, st *State) helm.RenderOptions {
	opts := in.renderOptions(in.BaseDir, in.BaseValues, in.BaseSet, in.BaseSetString, in.BaseSetFile, in.BaseRelease, in.BaseNamespace, in.BaseSkipCRDs)
	opts.SetStrings = append(opts.SetStrings, st.Placeholders...)
	return opts
}

func findingsFor(label string, diff helm.RenderDiff) []verdict.Finding {
	left := describeBin(diff.Left)
	right := describeBin(diff.Right)
	if diff.Failed {
		return []verdict.Finding{{
			RuleID:   "helm4.render_failed",
			Severity: verdict.SeverityBlocker,
			Message:  left + " and " + right + " were compared for " + label + ". One binary did not render it.",
			Detail:   diff.Failure + " https://helm.sh/docs/overview/#upgrading-to-helm-4",
		}}
	}
	if len(diff.Effects) == 0 {
		return nil
	}
	return []verdict.Finding{{
		RuleID:   "helm4.render_differs",
		Severity: verdict.SeverityWarning,
		Resource: singleObject(diff.Effects),
		Message:  left + " and " + right + " rendered different objects for " + label + ". " + describeEffects(diff.Effects),
		Detail:   "Fields that change between two renders of the same binary are ignored. The report names paths and does not print values. https://helm.sh/docs/overview/#upgrading-to-helm-4",
	}}
}

func describeBin(s helm.RenderSide) string {
	name := s.Bin
	if s.Version != "" {
		return name + " (" + s.Version + ")"
	}
	return name
}

func singleObject(effects []k8sdiff.Effect) string {
	if len(effects) == 0 {
		return ""
	}
	name := effects[0].Object
	for _, e := range effects[1:] {
		if e.Object != name {
			return ""
		}
	}
	return name
}

func describeEffects(effects []k8sdiff.Effect) string {
	const maxShown = 8
	parts := make([]string, 0, len(effects))
	for _, e := range effects {
		if e.Path == "" {
			parts = append(parts, e.Object+" is only in one render")
			continue
		}
		parts = append(parts, e.Object+" at "+e.Path)
	}
	extra := 0
	if len(parts) > maxShown {
		extra = len(parts) - maxShown
		parts = parts[:maxShown]
	}
	text := strings.Join(parts, "; ")
	if extra > 0 {
		text += fmt.Sprintf("; and %d more", extra)
	}
	return text + "."
}
