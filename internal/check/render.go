package check

import (
	"context"
	"fmt"
	"strings"

	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
)

func (in Input) renderOptions(dir string, values, sets, setStrings, setFiles []string, release, namespace string, skip bool) helm.RenderOptions {
	return helm.RenderOptions{
		ReleaseName: release,
		Namespace:   namespace,
		ChartDir:    dir,
		ValuesFiles: values,
		SetValues:   append(append([]string{}, sets...), in.SetValues...),
		SetStrings:  append([]string{}, setStrings...),
		SetFiles:    setFiles,
		KubeVersion: in.KubeVersion,
		SkipCRDs:    skip,
	}
}

// Charts renders the current chart, when there is one, and the new chart.
func Charts(ctx context.Context, in Input, st *State) {
	NoteChartAPI(in, st)
	var baseOpts helm.RenderOptions
	if in.BaseDir != "" {
		baseOpts = st.applyRepair(in.renderOptions(in.BaseDir, in.BaseValues, in.BaseSet, in.BaseSetString, in.BaseSetFile, in.BaseRelease, in.BaseNamespace, in.BaseSkipCRDs))
	}
	headOpts := st.applyRepair(in.renderOptions(in.HeadDir, in.HeadValues, in.HeadSet, in.HeadSetString, in.HeadSetFile, in.HeadRelease, in.HeadNamespace, in.HeadSkipCRDs))
	baseYAML, headYAML, repair, bErr, hErr := helm.CompleteCharts(ctx, "", baseOpts, headOpts)
	st.noteRepair(repair)
	if bErr != nil {
		secrets := helm.SecretStrings(in.BaseValues)
		secrets = append(secrets, helm.SecretValuesFromSets(in.BaseSet)...)
		secrets = append(secrets, helm.SecretValuesFromSets(in.BaseSetString)...)
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
			Name:    "baseline helm template",
			Status:  "fail",
			Summary: helm.Scrub(bErr.Error(), secrets),
		})
		st.stop(false, true)
		return
	}
	if in.BaseDir != "" {
		objs, pErr := k8sschema.ParseManifestStream(baseYAML)
		if pErr != nil {
			secrets := helm.SecretStrings(in.BaseValues)
			secrets = append(secrets, helm.SecretValuesFromSets(in.BaseSet)...)
			secrets = append(secrets, helm.SecretValuesFromSets(in.BaseSetString)...)
			st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{Name: "baseline manifest parse", Status: "fail", Summary: helm.Scrub(pErr.Error(), secrets)})
			st.stop(false, true)
			return
		}
		st.BaseObjs = objs
	}
	if len(repair.Keys()) > 0 || len(repair.APIVersions) > 0 || repair.SkipSchema {
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
			Name:    "chart defaults",
			Status:  "warn",
			Summary: repairSummary(repair),
		})
	}
	if hErr != nil {
		secrets := helm.SecretStrings(in.HeadValues)
		secrets = append(secrets, helm.SecretValuesFromSets(in.HeadSet)...)
		secrets = append(secrets, helm.SecretValuesFromSets(in.HeadSetString)...)
		if detail, ok := helm.RequiredFailure(hErr, secrets); ok {
			st.Report.Findings = append(st.Report.Findings, verdict.Finding{
				RuleID:   "helm.values.new_required_unset",
				Severity: verdict.SeverityBlocker,
				File:     in.HeadDir,
				Message:  "The new chart does not render because a required value is unset.",
				Detail:   detail,
				Proof:    "render",
			})
			st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
				Name:    "helm template",
				Status:  "fail",
				Summary: "a required value is unset",
			})
			st.stop(false, false)
			return
		}
		st.Report.Findings = append(st.Report.Findings, verdict.Finding{
			RuleID:   "helm.template.render_failed",
			Severity: verdict.SeverityBlocker,
			File:     in.HeadDir,
			Message:  "Candidate chart (`--to`) failed `helm template` rendering.",
			Detail:   helm.Scrub(hErr.Error(), secrets),
		})
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
			Name:    "helm template",
			Status:  "fail",
			Summary: "render failed",
		})
		st.stop(false, false)
		return
	}
	headObjs, hpErr := k8sschema.ParseManifestStream(headYAML)
	if hpErr != nil {
		secrets := helm.SecretStrings(in.HeadValues)
		secrets = append(secrets, helm.SecretValuesFromSets(in.HeadSet)...)
		secrets = append(secrets, helm.SecretValuesFromSets(in.HeadSetString)...)
		st.Report.Findings = append(st.Report.Findings, verdict.Finding{
			RuleID:   "helm.template.invalid_yaml",
			Severity: verdict.SeverityBlocker,
			File:     in.HeadDir,
			Message:  helm.Scrub(hpErr.Error(), secrets),
		})
		st.stop(false, false)
		return
	}
	st.HeadObjs = headObjs
	st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
		Name:    "helm template",
		Status:  "pass",
		Summary: fmt.Sprintf("rendered %s cleanly", counted(len(headObjs), "Kubernetes object", "Kubernetes objects")),
	})
}

func (s *State) applyRepair(opts helm.RenderOptions) helm.RenderOptions {
	if s == nil {
		return opts
	}
	opts.SetValues = append(append([]string{}, opts.SetValues...), s.RepairSets...)
	opts.SetStrings = append(append([]string{}, opts.SetStrings...), s.Placeholders...)
	opts.SetJSON = append(append([]string{}, opts.SetJSON...), s.RepairJSON...)
	opts.APIVersions = mergeStrings(append([]string{}, opts.APIVersions...), s.ServedAPIs)
	opts.APIVersions = mergeStrings(opts.APIVersions, s.RepairAPIs)
	if s.RepairSkipSchema {
		opts.SkipSchemaValidation = true
	}
	return opts
}

func (s *State) noteRepair(repair helm.Repair) {
	if s == nil || repair.Empty() {
		return
	}
	drop := map[string]bool{}
	for _, key := range repair.Remove {
		drop[key] = true
	}
	s.Placeholders = dropAssignmentKeys(dropParentAssignments(s.Placeholders, repair.Keys()), drop)
	s.RepairSets = dropAssignmentKeys(dropParentAssignments(s.RepairSets, repair.Keys()), drop)
	s.RepairJSON = dropAssignmentKeys(s.RepairJSON, drop)
	s.Placeholders = mergeAssignments(s.Placeholders, repair.SetStrings)
	s.RepairSets = mergeAssignments(s.RepairSets, repair.Sets)
	s.RepairJSON = mergeAssignments(s.RepairJSON, repair.SetJSON)
	s.RepairAPIs = mergeStrings(s.RepairAPIs, repair.APIVersions)
	if repair.SkipSchema {
		s.RepairSkipSchema = true
	}
}

func repairSummary(repair helm.Repair) string {
	var parts []string
	keys := repair.Keys()
	if len(keys) > 0 {
		parts = append(parts, fmt.Sprintf("the chart does not render unless %s is set: %s", counted(len(keys), "this value", "these values"), strings.Join(keys, ", ")))
	}
	if len(repair.APIVersions) > 0 {
		parts = append(parts, "assumed Kubernetes APIs "+strings.Join(repair.APIVersions, ", "))
	}
	if repair.SkipSchema {
		parts = append(parts, "the chart schema document is invalid, so schema validation was skipped")
	}
	return strings.Join(parts, "; ")
}

func dropAssignmentKeys(assignments []string, drop map[string]bool) []string {
	if len(drop) == 0 {
		return assignments
	}
	var out []string
	for _, assignment := range assignments {
		key, _, _ := strings.Cut(assignment, "=")
		if drop[key] {
			continue
		}
		out = append(out, assignment)
	}
	return out
}

func dropParentAssignments(assignments, childKeys []string) []string {
	var out []string
	for _, assignment := range assignments {
		key, _, _ := strings.Cut(assignment, "=")
		drop := false
		for _, child := range childKeys {
			if strings.HasPrefix(child, key+".") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, assignment)
		}
	}
	return out
}

func mergeAssignments(have, add []string) []string {
	seen := map[string]bool{}
	for _, assignment := range have {
		key, _, _ := strings.Cut(assignment, "=")
		seen[key] = true
	}
	for _, assignment := range add {
		key, _, ok := strings.Cut(assignment, "=")
		if !ok || key == "" || seen[key] {
			continue
		}
		seen[key] = true
		have = append(have, assignment)
	}
	return have
}

func mergeStrings(have, add []string) []string {
	seen := map[string]bool{}
	for _, item := range have {
		seen[item] = true
	}
	for _, item := range add {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		have = append(have, item)
	}
	return have
}
