package check

import (
	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/verdict"
)

// Changes names chart, image, and dependency changes. It does not block.
func Changes(in Input, st *State) {
	if in.BaseDir == "" {
		return
	}
	signals := helm.UpgradeSignals(in.BaseDir, in.HeadDir, st.BaseObjs, st.HeadObjs)
	if len(signals) == 0 {
		return
	}
	st.Report.Findings = append(st.Report.Findings, signals...)
	st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
		Name:    "upgrade signals",
		Status:  "warn",
		Summary: counted(len(signals), "named change", "named changes"),
	})
}
