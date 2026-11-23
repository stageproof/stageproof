package check

import (
	"context"
	"strings"

	"github.com/stageproof/stageproof/internal/config"
	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/rehearse"
	"github.com/stageproof/stageproof/internal/verdict"
)

// Rehearse installs, upgrades, and rolls back when --live was asked for and
// nothing is already blocked.
func Rehearse(ctx context.Context, in Input, st *State) (runtimeOK, inconclusive bool) {
	if !in.Live || countBlockers(st.Report.Findings) > 0 {
		return false, false
	}
	extCRDs := k8sschema.DetectExternalCRDs(append(st.BaseObjs, st.HeadObjs...))
	outcome, rErr := rehearse.RunKindRehearsal(ctx, rehearse.KindOptions{
		KubernetesVersion:    in.KubeVersion,
		ClusterName:          in.ClusterName,
		KeepCluster:          in.KeepCluster,
		BaseChartDir:         in.BaseDir,
		HeadChartDir:         in.HeadDir,
		BaseValues:           in.BaseValues,
		HeadValues:           in.HeadValues,
		SetValues:            append(append([]string{}, in.SetValues...), st.RepairSets...),
		SetStrings:           st.Placeholders,
		SetJSON:              st.RepairJSON,
		APIVersions:          st.RepairAPIs,
		SkipSchemaValidation: st.RepairSkipSchema,
		ExternalCRDs:         extCRDs,
		ExtraCRDFiles:        in.CRDFiles,
		Timeout:              in.Timeout,
		RunHelmTest:          in.HelmTest,
		Smoke:                smokeTests(in),
		Secrets:              rehearsalSecrets(in),
	})
	if rErr != nil {
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{Name: "kind live test", Status: "fail", Summary: rErr.Error()})
		return false, true
	}
	if outcome.Rehearsed {
		st.Report.Checked = noteLiveChecked(st.Report.Checked)
	}
	st.Report.Checks = append(st.Report.Checks, outcome.Checks...)
	st.Report.Findings = append(st.Report.Findings, outcome.Findings...)
	st.Report.Skipped = append(st.Report.Skipped, outcome.Degraded...)
	st.Report.ExcludedChecks = append(st.Report.ExcludedChecks, outcome.ExcludedChecks...)
	if outcome.ClusterName != "" {
		st.Report.Cluster = outcome.ClusterName
	}
	return outcome.RuntimeSucceeded, outcome.Inconclusive
}

func smokeTests(in Input) []config.SmokeTest {
	if in.Config == nil {
		return nil
	}
	return in.Config.Smoke
}

func rehearsalSecrets(in Input) []string {
	var secrets []string
	secrets = append(secrets, helm.SecretStrings(in.BaseValues)...)
	secrets = append(secrets, helm.SecretStrings(in.HeadValues)...)
	secrets = append(secrets, helm.SecretValuesFromSets(in.SetValues)...)
	secrets = append(secrets, helm.SecretValuesFromSets(in.BaseSet)...)
	secrets = append(secrets, helm.SecretValuesFromSets(in.HeadSet)...)
	secrets = append(secrets, helm.SecretValuesFromSets(in.BaseSetString)...)
	secrets = append(secrets, helm.SecretValuesFromSets(in.HeadSetString)...)
	return secrets
}

func noteLiveChecked(checked string) string {
	if strings.Contains(checked, "live cluster") {
		return checked
	}
	if checked == "" {
		return verdict.CheckedFilesAndLive
	}
	return checked + ", live cluster"
}
