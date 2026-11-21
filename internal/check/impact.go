package check

import (
	"github.com/stageproof/stageproof/internal/impact"
	"github.com/stageproof/stageproof/internal/verdict"
)

// Impact names deletes, restarts, exposure, privilege, capacity, and CRD
// breaks. It uses stored objects when the API server returned them.
func Impact(in Input, st *State) {
	// Manifest mode has no chart directory. The objects come from --before and
	// --after. A chart check with no current chart still has nothing to compare.
	if !in.ManifestMode && in.BaseDir == "" {
		return
	}
	var stored map[string][]string
	if in.Facts != nil {
		stored = in.Facts.StoredVersions()
	}
	findings, summary := impact.Compare(st.BaseObjs, st.HeadObjs, impact.Options{
		Adapter:       st.Report.Adapter,
		FluxCRDPolicy: in.FluxCRDPolicy,
		ArgoSkipCrds:  in.ArgoSkipCrds,
		BeforeDir:     in.BaseDir,
		AfterDir:      in.HeadDir,
		APIServerRan:  st.APIServerRan,
		StoredBefore:  st.StoredBefore,
		StoredAfter:   st.StoredAfter,
		ClusterStored: stored,
	})
	blockers, warns := 0, 0
	for _, f := range findings {
		if f.RuleID == "crd.existing_object_invalid" && findingExists(st.Report.Findings, f.RuleID, f.Resource) {
			continue
		}
		st.Report.Findings = append(st.Report.Findings, f)
		switch f.Severity {
		case verdict.SeverityBlocker:
			blockers++
		case verdict.SeverityWarning:
			warns++
		}
	}
	status := "pass"
	if blockers > 0 {
		status = "fail"
	} else if warns > 0 {
		status = "warn"
	}
	st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
		Name:    "impact",
		Status:  status,
		Summary: summary,
	})
}

func findingExists(in []verdict.Finding, rule, resource string) bool {
	for _, f := range in {
		if f.RuleID == rule && f.Resource == resource {
			return true
		}
	}
	return false
}
