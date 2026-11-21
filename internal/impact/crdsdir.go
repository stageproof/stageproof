package impact

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
)

// Helm, Argo CD, and Flux do not treat crds/ the same way. The sentences
// below were checked against the current docs on 2026-11-20.
//
// Helm: https://helm.sh/docs/topics/charts/
// "CRDs are never reinstalled." "CRDs are never installed on upgrade or
// rollback." "CRDs are never deleted."
//
// Argo CD: https://argo-cd.readthedocs.io/en/stable/user-guide/helm/
// "Helm is only used to inflate charts with helm template. The lifecycle of
// the application is handled by Argo CD instead of Helm." The same page
// documents spec.source.helm.skipCrds. It also says Helm installs CRDs in
// crds/ when they are not already present.
//
// Flux: https://fluxcd.io/flux/components/helm/api/v2/
// spec.upgrade.crds is Skip, Create, or CreateReplace. The default on upgrade
// is Skip: CRDs are neither installed nor upgraded.

func crdsDirectory(before []obj, opt Options) []verdict.Finding {
	oldFiles := loadDirCRDs(opt.BeforeDir)
	newFiles := loadDirCRDs(opt.AfterDir)
	if len(oldFiles) == 0 && len(newFiles) == 0 {
		return nil
	}
	mode := crdDeployMode(opt.Adapter, opt.FluxCRDPolicy, opt.ArgoSkipCrds)
	names := map[string]bool{}
	for n := range oldFiles {
		names[n] = true
	}
	for n := range newFiles {
		names[n] = true
	}
	var ordered []string
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)

	var users []obj
	for _, o := range before {
		if o.Kind != "CustomResourceDefinition" {
			users = append(users, o)
		}
	}
	var findings []verdict.Finding
	for _, name := range ordered {
		old, hasOld := oldFiles[name]
		neu, hasNew := newFiles[name]
		if hasOld && hasNew && fingerprint(old) == fingerprint(neu) {
			continue
		}
		switch {
		case !hasOld && hasNew:
			findings = append(findings, directoryFinding(mode, name, false, "the file is new"))
		case hasOld && !hasNew:
			findings = append(findings, directoryFinding(mode, name, false, "the file was removed"))
		default:
			changes, err := schemas.Compare(old, neu, opt.storedFor(name))
			why := "the file contents changed"
			incompatible := false
			if err == nil && len(changes) > 0 {
				incompatible = true
				why = describeChanges(changes)
			}
			findings = append(findings, directoryFinding(mode, name, incompatible, why))
			if mode.appliesSchema && hasOld && hasNew {
				oldObj := obj{ID: "CustomResourceDefinition/default/" + name, Name: name, Kind: "CustomResourceDefinition", Body: old}
				newObj := oldObj
				newObj.Body = neu
				findings = append(findings, compareCRD(oldObj, newObj, users, opt.storedFor(name))...)
			}
		}
	}
	return findings
}

type deployMode struct {
	rule          string
	appliesSchema bool
	helmLike      bool
	fluxCreate    bool
	fluxReplace   bool
	argocd        bool
	argocdKnown   bool
	argocdSkip    bool
}

func crdDeployMode(adapter, policy string, skipCrds *bool) deployMode {
	switch adapter {
	case "argocd":
		// skipCrds true means Argo CD does not apply crds/, same as plain Helm.
		// A non-nil false means the Application set the field. Nil means the
		// field was absent, so the warning says the input does not say.
		if skipCrds != nil && *skipCrds {
			return deployMode{rule: "crd.not_upgraded_by_helm", helmLike: true, argocdSkip: true}
		}
		return deployMode{rule: "crd.argocd_renders_crds", appliesSchema: true, argocd: true, argocdKnown: skipCrds != nil}
	case "flux":
		switch policy {
		case "CreateReplace":
			return deployMode{rule: "crd.flux_upgrade_crds", appliesSchema: true, fluxReplace: true}
		case "Create":
			return deployMode{rule: "crd.flux_upgrade_crds", fluxCreate: true}
		default:
			return deployMode{rule: "crd.flux_upgrade_crds", helmLike: true}
		}
	default:
		return deployMode{rule: "crd.not_upgraded_by_helm", helmLike: true}
	}
}

func directoryFinding(mode deployMode, name string, incompatible bool, why string) verdict.Finding {
	sev := verdict.SeverityWarning
	if incompatible && (mode.helmLike || mode.fluxCreate) {
		sev = verdict.SeverityBlocker
	}
	msg := helmDirectoryMessage(name, why)
	switch {
	case mode.argocdSkip:
		msg = fmt.Sprintf("The CRD %s under crds/ changed (%s). spec.source.helm.skipCrds is true, so Argo CD does not apply CRDs in that directory. helm upgrade leaves the CRD already in the cluster as it is.", name, why)
	case mode.argocd && mode.argocdKnown:
		msg = fmt.Sprintf("The CRD %s under crds/ changed (%s). spec.source.helm.skipCrds is false, so Argo CD renders the chart with helm template and applies CRDs in crds/.", name, why)
		sev = verdict.SeverityWarning
	case mode.argocd:
		msg = fmt.Sprintf("The CRD %s under crds/ changed (%s). Argo CD renders the chart with helm template and applies the manifests itself, so CRDs in crds/ are part of the sync unless spec.source.helm.skipCrds is true. This input does not say whether skipCrds is set.", name, why)
		sev = verdict.SeverityWarning
	case mode.fluxReplace:
		msg = fmt.Sprintf("The CRD %s under crds/ changed (%s). Flux spec.upgrade.crds is CreateReplace, so an existing CRD in that directory is updated and is not deleted.", name, why)
		sev = verdict.SeverityInfo
	case mode.rule == "crd.flux_upgrade_crds" && mode.fluxCreate:
		msg = fmt.Sprintf("The CRD %s under crds/ changed (%s). Flux spec.upgrade.crds is Create. A new CRD is installed. An existing CRD is left as it is.", name, why)
	case mode.rule == "crd.flux_upgrade_crds":
		msg = fmt.Sprintf("The CRD %s under crds/ changed (%s). Flux spec.upgrade.crds defaults to Skip when it is omitted, so CRDs in that directory are neither installed nor upgraded.", name, why)
	}
	return verdict.Finding{RuleID: mode.rule, Severity: sev, Resource: "CustomResourceDefinition/default/" + name, Message: msg}
}

func helmDirectoryMessage(name, why string) string {
	return fmt.Sprintf("The CRD %s under crds/ changed (%s). Plain Helm does not upgrade CRDs in that directory. helm upgrade leaves the CRD already in the cluster as it is, and CRDs are not installed on upgrade or rollback.", name, why)
}

func describeChanges(changes []schemaChange) string {
	var parts []string
	for _, c := range changes {
		prop := strings.TrimPrefix(c.Property, "^.")
		prop = strings.TrimPrefix(prop, "^")
		if prop != "" {
			parts = append(parts, prop+" "+c.Class)
		} else if c.Class == "stored" {
			parts = append(parts, "storage version removed")
		} else {
			parts = append(parts, c.Class)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func loadDirCRDs(dir string) map[string]map[string]any {
	if dir == "" {
		return nil
	}
	files := chartCRDFiles(dir)
	if len(files) == 0 {
		return nil
	}
	var buf strings.Builder
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) == 0 {
			continue
		}
		buf.Write(raw)
		buf.WriteString("\n---\n")
	}
	if buf.Len() == 0 {
		return nil
	}
	objs, err := k8sschema.ParseManifestStream(buf.String())
	if err != nil {
		return nil
	}
	out := map[string]map[string]any{}
	for _, o := range objs {
		if o.Kind == "CustomResourceDefinition" && o.Metadata.Name != "" && o.Object != nil {
			out[o.Metadata.Name] = o.Object
		}
	}
	return out
}

func chartCRDFiles(chartDir string) []string {
	roots := []string{filepath.Join(chartDir, "crds")}
	entries, err := os.ReadDir(filepath.Join(chartDir, "charts"))
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				roots = append(roots, filepath.Join(chartDir, "charts", entry.Name(), "crds"))
			}
		}
	}
	var files []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".yaml", ".yml", ".json":
				files = append(files, path)
			}
			return nil
		})
	}
	sort.Strings(files)
	return files
}

func fingerprint(body map[string]any) string {
	cloned, ok := cloneAny(body).(map[string]any)
	if !ok {
		return canonical(body)
	}
	delete(cloned, "status")
	return canonical(cloned)
}

// withoutDirectoryCRDs drops CRDs that Helm does not track as release objects.
// Argo CD applies the rendered manifest, including those files, so they stay.
func withoutDirectoryCRDs(in map[string]obj, dir, adapter string) map[string]obj {
	if adapter == "argocd" || dir == "" {
		return in
	}
	files := loadDirCRDs(dir)
	if len(files) == 0 {
		return in
	}
	specs := map[string]string{}
	for name, body := range files {
		specs[name] = canonical(specOf(body))
	}
	out := map[string]obj{}
	for k, o := range in {
		if o.Kind == "CustomResourceDefinition" && specs[o.Name] != "" && specs[o.Name] == canonical(specOf(o.Body)) {
			continue
		}
		out[k] = o
	}
	return out
}
