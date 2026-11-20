package impact

import (
	"fmt"

	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
)

// Options is the comparison context. Adapter is the tool that produced the
// objects. FluxCRDPolicy is spec.upgrade.crds when the Flux adapter has read
// it. Empty means the Flux default, Skip.
type Options struct {
	Adapter       string
	FluxCRDPolicy string
	BeforeDir     string
	AfterDir      string
	APIServerRan  bool
	Stored        [][]byte
	ClusterStored map[string][]string
}

func (o Options) storedFor(crdName string) []string {
	if o.ClusterStored == nil {
		return nil
	}
	return o.ClusterStored[crdName]
}

// Compare reads two sets of objects and returns impact and CRD findings.
// The summary says whether the objects were the stored form from the API
// server or the rendered YAML.
func Compare(before, after []k8sschema.ManifestObject, opt Options) ([]verdict.Finding, string) {
	if opt.Adapter == "" {
		opt.Adapter = "helm"
	}
	rawBefore := withoutDirectoryCRDs(indexManifests(before), opt.BeforeDir, opt.Adapter)
	rawAfter := withoutDirectoryCRDs(indexManifests(after), opt.AfterDir, opt.Adapter)
	chosen, summary := chooseForms(rawBefore, rawAfter, opt)

	var findings []verdict.Finding
	findings = append(findings, deletes(rawBefore, rawAfter)...)
	findings = append(findings, crdSafety(rawBefore, rawAfter, opt)...)
	findings = append(findings, crdsDirectory(rawBeforeList(rawBefore), opt)...)
	findings = append(findings, privilege(chosen.before, chosen.after)...)
	findings = append(findings, exposure(chosen.before, chosen.after)...)
	findings = append(findings, restarts(chosen.before, chosen.after)...)
	findings = append(findings, capacity(chosen.before, chosen.after)...)
	return findings, summary
}

func rawBeforeList(in map[string]obj) []obj {
	out := make([]obj, 0, len(in))
	for _, o := range in {
		out = append(out, o)
	}
	return out
}

type formSet struct {
	before map[string]obj
	after  map[string]obj
}

func chooseForms(rawBefore, rawAfter map[string]obj, opt Options) (formSet, string) {
	storedBefore, storedAfter := splitStored(rawBefore, rawAfter, opt.Stored)
	f := formSet{before: map[string]obj{}, after: map[string]obj{}}
	paired, fromStored := 0, 0
	seen := map[string]bool{}
	for k := range rawBefore {
		seen[k] = true
	}
	for k := range rawAfter {
		seen[k] = true
	}
	for k := range seen {
		old, hasOld := rawBefore[k]
		neu, hasNew := rawAfter[k]
		if hasOld && hasNew {
			paired++
			sb, okB := storedBefore[k]
			sa, okA := storedAfter[k]
			if okB && okA {
				f.before[k] = sb
				f.after[k] = sa
				fromStored++
				continue
			}
			f.before[k] = old
			f.after[k] = neu
			continue
		}
		if hasOld {
			f.before[k] = old
		}
		if hasNew {
			f.after[k] = neu
		}
	}
	return f, sourceSummary(opt.APIServerRan, paired, fromStored)
}

func splitStored(rawBefore, rawAfter map[string]obj, blobs [][]byte) (map[string]obj, map[string]obj) {
	before := map[string]obj{}
	after := map[string]obj{}
	seen := map[string]int{}
	for _, blob := range blobs {
		o, ok := fromJSON(blob)
		if !ok {
			continue
		}
		_, inBefore := rawBefore[o.Key]
		_, inAfter := rawAfter[o.Key]
		if !inBefore && !inAfter {
			continue
		}
		seen[o.Key]++
		if inBefore && inAfter {
			if seen[o.Key] == 1 {
				before[o.Key] = o
			} else {
				after[o.Key] = o
			}
			continue
		}
		if inBefore {
			before[o.Key] = o
		}
		if inAfter {
			after[o.Key] = o
		}
	}
	return before, after
}

func sourceSummary(api bool, paired, stored int) string {
	if !api {
		return "Compared rendered objects. The API server did not run, so a default the server adds is not in this comparison."
	}
	if paired == 0 {
		return "Compared rendered objects. No object is in both charts to diff as a stored form."
	}
	if stored == paired {
		return "Compared stored objects from the API server."
	}
	if stored == 0 {
		return "Compared rendered objects. The API server did not return a stored form for both sides of any object."
	}
	return fmt.Sprintf("Compared stored objects for %d of %d objects, and rendered objects for the rest.", stored, paired)
}
