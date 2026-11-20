package impact

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/verdict"
)

func deletes(before, after map[string]obj) []verdict.Finding {
	var created []obj
	for k, o := range after {
		if _, ok := before[k]; !ok {
			created = append(created, o)
		}
	}
	used := map[string]bool{}
	var gone []obj
	for k, o := range before {
		if _, ok := after[k]; ok || kept(o) {
			continue
		}
		gone = append(gone, o)
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].ID < gone[j].ID })

	var findings []verdict.Finding
	var other []string
	for _, o := range gone {
		repl, replaced := replacement(o, created, used)
		switch o.Kind {
		case "CustomResourceDefinition":
			findings = append(findings, deleteFinding("impact.delete.crd", verdict.SeverityBlocker, o, repl, replaced,
				fmt.Sprintf("Helm will delete %s. A templated custom resource definition that the new chart does not render is removed, and Kubernetes then deletes every object of that kind.", o.ID)))
		case "PersistentVolumeClaim":
			findings = append(findings, deleteFinding("impact.delete.pvc", verdict.SeverityBlocker, o, repl, replaced,
				fmt.Sprintf("Helm will delete %s. The data on that claim goes with it.", o.ID)))
		case "Namespace":
			findings = append(findings, deleteFinding("impact.delete.namespace", verdict.SeverityBlocker, o, repl, replaced,
				fmt.Sprintf("Helm will delete %s. Every object in that namespace is deleted with it.", o.ID)))
		case "StatefulSet":
			sev := verdict.SeverityWarning
			msg := fmt.Sprintf("Helm will delete %s. persistentVolumeClaimRetentionPolicy.whenDeleted is not Delete, so the claims created for it stay.", o.ID)
			if whenDeleted(o) == "Delete" {
				sev = verdict.SeverityBlocker
				msg = fmt.Sprintf("Helm will delete %s. persistentVolumeClaimRetentionPolicy.whenDeleted is Delete, so the claims created for it are deleted too.", o.ID)
			}
			findings = append(findings, deleteFinding("impact.delete.statefulset", sev, o, repl, replaced, msg))
		case "NetworkPolicy":
			// Exposure reports a removed network policy. It is not a second delete line.
		default:
			if replaced {
				findings = append(findings, deleteFinding("impact.delete", verdict.SeverityWarning, o, repl, true,
					fmt.Sprintf("Helm will delete %s.", o.ID)))
			} else {
				other = append(other, o.ID)
			}
		}
	}
	if len(other) > 0 {
		sort.Strings(other)
		findings = append(findings, verdict.Finding{
			RuleID:   "impact.delete",
			Severity: verdict.SeverityWarning,
			Message:  "Helm will delete objects the new chart does not render: " + strings.Join(other, ", ") + ".",
		})
	}
	return findings
}

func deleteFinding(id string, sev verdict.Severity, old, repl obj, replaced bool, msg string) verdict.Finding {
	if replaced {
		msg = "replaced: delete then create. " + msg + " " + repl.ID + " is created in its place."
	}
	return verdict.Finding{RuleID: id, Severity: sev, Resource: old.ID, Message: msg}
}

func replacement(old obj, created []obj, used map[string]bool) (obj, bool) {
	want := signature(old)
	if want == "" {
		return obj{}, false
	}
	for _, c := range created {
		if used[c.Key] || c.Group != old.Group || c.Kind != old.Kind {
			continue
		}
		if signature(c) == want {
			used[c.Key] = true
			return c, true
		}
	}
	return obj{}, false
}

func whenDeleted(o obj) string {
	pol, _ := specOf(o.Body)["persistentVolumeClaimRetentionPolicy"].(map[string]any)
	if pol == nil {
		return ""
	}
	s, _ := pol["whenDeleted"].(string)
	return s
}
