package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/yaml"

	"github.com/stageproof/stageproof/internal/verdict"
)

const fieldManager = "stageproof"

const (
	ruleRemoved    = "k8s.api.removed"
	ruleEOL        = "k8s.api.eol_blocker"
	ruleImmutable  = "k8s.apply.immutable_field"
	ruleUnknown    = "k8s.apply.unknown_field"
	ruleInvalid    = "k8s.apply.invalid"
	ruleExisting   = "crd.existing_object_invalid"
	ruleNotSent    = "k8s.apply.not_exercised"
	hookAnnotation = "helm.sh/hook"
)

// Manifest is one rendered Kubernetes document.
type Manifest struct {
	Raw map[string]any
}

// ProofOptions selects which half of the upgrade is being asked.
type ProofOptions struct {
	// Preload is the cluster's custom resource definitions, from a facts file.
	// They are installed before the chart's definitions. This does not write
	// them back to the cluster the facts came from.
	Preload []Manifest
	// StrictUnknown is set when this apply uses server-side apply.
	// Helm 3 client-side apply drops an unknown field and succeeds.
	// A new Helm 4 install uses server-side apply. An upgrade of a Helm 3
	// release stays client-side unless the caller sets this.
	// https://helm.sh/docs/overview/#server-side-apply
	StrictUnknown bool
	// CreatesOnly dry-runs every object as a create. The next Kubernetes
	// version has no current objects to update.
	CreatesOnly bool
	// Warnings turns every rejection into a warning. Used for --next-k8s.
	Warnings bool
}

// ProofResult is what the API server demonstrated.
type ProofResult struct {
	Findings []verdict.Finding
	// StoredBefore is the JSON of each current object the server accepted.
	StoredBefore [][]byte
	// StoredAfter is the JSON of each new object the dry run returned.
	StoredAfter [][]byte
}

// Prove installs the current CRDs, applies the current objects, upgrades the
// CRDs, dry-runs the new objects, and then checks each stored custom resource
// against the new schema.
// beforeExtra and afterExtra are CRDs from each chart's crds/ directory.
// They stay on their own side so an updated file is not replaced by the old copy.
func (s *Server) Prove(ctx context.Context, before, after, beforeExtra, afterExtra []Manifest, opt ProofOptions) (ProofResult, error) {
	var result ProofResult
	beforeObjs := docs(before)
	afterObjs := docs(after)
	beforeExtraObjs := docs(beforeExtra)
	afterExtraObjs := docs(afterExtra)

	var notSent []string
	keep := func(u *unstructured.Unstructured) bool {
		if u == nil || u.GetKind() == "" || strings.HasSuffix(u.GetKind(), "List") {
			return false
		}
		if u.GetName() == "" {
			notSent = append(notSent, u.GetKind())
			return false
		}
		if skippedKind(u.GetKind()) {
			notSent = append(notSent, objectID(u))
			return false
		}
		return true
	}

	beforeCRDs, beforeRest := splitCRDs(filter(beforeObjs, keep))
	afterCRDs, afterRest := splitCRDs(filter(afterObjs, keep))
	for _, u := range filter(beforeExtraObjs, keep) {
		if u.GetKind() == "CustomResourceDefinition" {
			beforeCRDs = append(beforeCRDs, u)
		}
	}
	for _, u := range filter(afterExtraObjs, keep) {
		if u.GetKind() == "CustomResourceDefinition" {
			afterCRDs = append(afterCRDs, u)
		}
	}
	beforeCRDs = dedupe(beforeCRDs)
	afterCRDs = dedupe(afterCRDs)

	// CRDs first, and wait until they are Established, before any custom
	// resource is sent. A dry run before that returns "no matches for kind".
	if err := s.refreshMapper(); err != nil {
		return result, err
	}
	installed := map[string]*unstructured.Unstructured{}
	if len(opt.Preload) > 0 {
		var preload []*unstructured.Unstructured
		for _, u := range filter(docs(opt.Preload), keep) {
			if u.GetKind() == "CustomResourceDefinition" {
				preload = append(preload, withoutConversion(u))
			}
		}
		// A preload failure is not a finding about the chart.
		var ignored ProofResult
		s.installCRDs(ctx, preload, installed, &ignored, opt)
	}
	if opt.CreatesOnly {
		s.installCRDs(ctx, afterCRDs, installed, &result, opt)
	} else {
		// Installing the current chart's CRDs is setup for the upgrade check.
		// A defect in the old chart must not block upgrading to a fixed new chart.
		var ignored ProofResult
		s.installCRDs(ctx, beforeCRDs, installed, &ignored, opt)
	}
	if err := s.refreshMapper(); err != nil {
		return result, err
	}

	if err := s.ensureNamespaces(ctx, beforeRest, afterRest, opt.CreatesOnly); err != nil {
		return result, err
	}

	applied := map[string]bool{}
	beforeInvalid := map[string]map[string]bool{}
	if !opt.CreatesOnly {
		// A hook in the new chart is created on its own. Helm does not update
		// a same-named object in place, so the current object is not applied
		// when the new one is a hook. Applying it would turn the dry run into
		// an update, and Job pod templates would block the hook.
		afterHook := map[string]bool{}
		for _, u := range afterRest {
			if hook(u) {
				afterHook[key(u)] = true
			}
		}
		for _, u := range beforeRest {
			if hook(u) || afterHook[key(u)] {
				continue
			}
			// Installing the current chart is setup. A validation failure on the
			// old chart must not block upgrading to a chart that fixes it, and a
			// pre-existing invalid default on the same fields is recorded so
			// afterRest can downgrade it to a warning rather than a false block.
			got, err := s.apply(ctx, u, false)
			if err != nil {
				cls := Classify(err)
				if cls.Class == ClassInvalid && len(cls.Fields) > 0 {
					set := map[string]bool{}
					for _, f := range cls.Fields {
						set[f] = true
					}
					beforeInvalid[key(u)] = set
				}
				continue
			}
			if got != nil {
				applied[key(u)] = true
				if raw, err := json.Marshal(got.Object); err == nil {
					result.StoredBefore = append(result.StoredBefore, raw)
				}
			}
		}

		// Current objects are stored under the old CRDs. Upgrade the CRDs
		// only after that, then check the stored custom resources.
		byName := map[string]*unstructured.Unstructured{}
		for name, crd := range installed {
			byName[name] = crd
		}
		for _, crd := range beforeCRDs {
			byName[crd.GetName()] = crd
		}
		for _, crd := range afterCRDs {
			old := byName[crd.GetName()]
			if old != nil && specEqual(old, crd) {
				continue
			}
			// Dry-run the new CRD against the one already stored. This is
			// where removing a stored version is rejected. Then apply it.
			if old != nil {
				if _, finding, _ := s.putCRD(ctx, crd, true, opt); finding != nil {
					result.Findings = append(result.Findings, *finding)
					continue
				}
			}
			got, finding, skip := s.putCRD(ctx, crd, false, opt)
			if finding != nil {
				result.Findings = append(result.Findings, *finding)
			}
			if skip || got == nil {
				continue
			}
			installed[crd.GetName()] = got
		}
		if err := s.refreshMapper(); err != nil {
			return result, err
		}
	}

	for _, u := range afterRest {
		// Dry-run only. A hook was not stored, so this apply is a create.
		// A stored object is an update, which is what rejects an immutable field.
		got, err := s.apply(ctx, u, true)
		if err != nil {
			finding, _ := s.finding(ctx, u, err, opt, false)
			if finding != nil {
				if !opt.CreatesOnly && finding.RuleID == ruleInvalid {
					cls := Classify(err)
					if pre := beforeInvalid[key(u)]; len(pre) > 0 && len(cls.Fields) > 0 && allIn(cls.Fields, pre) {
						finding.Severity = verdict.SeverityWarning
						finding.Message += " The current chart failed the same validation; the upgrade did not introduce it."
					}
				}
				result.Findings = append(result.Findings, *finding)
			}
			continue
		}
		if got != nil {
			if raw, err := json.Marshal(got.Object); err == nil {
				result.StoredAfter = append(result.StoredAfter, raw)
			}
		}
	}

	if !opt.CreatesOnly {
		crdKinds := crdGroupKinds(installed)
		for _, u := range beforeRest {
			if hook(u) || !applied[key(u)] {
				continue
			}
			gk := schema.GroupKind{Group: u.GroupVersionKind().Group, Kind: u.GetKind()}
			if _, ok := crdKinds[gk]; !ok {
				continue
			}
			_, finding := s.putExisting(ctx, u, opt)
			if finding != nil {
				result.Findings = append(result.Findings, *finding)
			}
		}
	}

	if len(notSent) > 0 {
		result.Findings = append(result.Findings, verdict.Finding{
			RuleID:   ruleNotSent,
			Severity: verdict.SeverityInfo,
			Message:  "Not sent to the API server: " + strings.Join(notSent, ", ") + ".",
			Detail:   "Webhook configurations and API services would call an address that is not running. Objects without a name cannot be applied.",
		})
	}
	return result, nil
}

func (s *Server) putCRD(ctx context.Context, u *unstructured.Unstructured, dry bool, opt ProofOptions) (*unstructured.Unstructured, *verdict.Finding, bool) {
	got, err := s.apply(ctx, u, dry)
	if err == nil && !dry {
		if err := s.waitEstablished(ctx, u.GetName()); err != nil {
			f := notChecked(u, "the CRD was not established")
			return nil, &f, true
		}
	}
	if err == nil {
		return got, nil, false
	}
	f, skip := s.finding(ctx, u, err, opt, false)
	if f == nil {
		return nil, nil, skip
	}
	return nil, f, skip
}

func allIn(fields []string, allowed map[string]bool) bool {
	for _, f := range fields {
		if !allowed[f] {
			return false
		}
	}
	return true
}

func (s *Server) putExisting(ctx context.Context, u *unstructured.Unstructured, opt ProofOptions) (*unstructured.Unstructured, *verdict.Finding) {
	live, err := s.getObject(ctx, u)
	if err != nil {
		f, _ := s.finding(ctx, u, err, opt, true)
		return nil, f
	}
	// A full Update of an object that did not change does not recheck the CRD
	// type. Server-side apply builds a typed object and does. managedFields
	// has to be absent or that apply is rejected before the schema is read.
	body := live.DeepCopy()
	unstructured.RemoveNestedField(body.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(body.Object, "status")
	_, err = s.apply(ctx, body, true)
	if err == nil {
		return live, nil
	}
	f, _ := s.finding(ctx, u, err, opt, true)
	return nil, f
}

func withoutConversion(crd *unstructured.Unstructured) *unstructured.Unstructured {
	if crd == nil {
		return nil
	}
	body := crd.DeepCopy()
	unstructured.RemoveNestedField(body.Object, "spec", "conversion")
	_ = unstructured.SetNestedMap(body.Object, map[string]any{"strategy": "None"}, "spec", "conversion")
	return body
}

func (s *Server) installCRDs(ctx context.Context, crds []*unstructured.Unstructured, installed map[string]*unstructured.Unstructured, result *ProofResult, opt ProofOptions) {
	for _, crd := range crds {
		got, finding, skip := s.putCRD(ctx, crd, false, opt)
		if finding != nil {
			result.Findings = append(result.Findings, *finding)
		}
		if skip || got == nil {
			continue
		}
		installed[crd.GetName()] = got
	}
}

func (s *Server) ensureNamespaces(ctx context.Context, before, after []*unstructured.Unstructured, createsOnly bool) error {
	ns := map[string]bool{}
	collect := func(list []*unstructured.Unstructured) {
		for _, u := range list {
			if u.GetNamespace() != "" {
				ns[u.GetNamespace()] = true
			}
		}
	}
	collect(after)
	if !createsOnly {
		collect(before)
	}
	for name := range ns {
		if err := s.ensureNamespace(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) finding(ctx context.Context, u *unstructured.Unstructured, err error, opt ProofOptions, existing bool) (*verdict.Finding, bool) {
	class := Classify(err)
	if class.Class == ClassNoMatch {
		known, served, gerr := s.groupVersion(ctx, u.GroupVersionKind().Group, u.GroupVersionKind().Version)
		if gerr != nil || !known || served {
			// The group is not one this server ships, or the lookup itself failed.
			// That is a CRD this chart did not install, not a removed API.
			f := notChecked(u, "the API group is not installed on this server")
			return &f, true
		}
		rule := ruleRemoved
		sev := verdict.SeverityBlocker
		msg := fmt.Sprintf("API `%s` (%s) is not served by Kubernetes %s.", u.GetAPIVersion(), u.GetKind(), s.version)
		if opt.Warnings {
			rule = ruleEOL
			sev = verdict.SeverityWarning
			msg = fmt.Sprintf("API `%s` (%s) is not served by the next Kubernetes (%s).", u.GetAPIVersion(), u.GetKind(), s.version)
		}
		return &verdict.Finding{RuleID: rule, Severity: sev, Resource: objectID(u), Message: msg}, false
	}
	if class.Class == ClassUnclassified || class.Class == ClassNone {
		f := notChecked(u, "the API server did not return a field error")
		return &f, true
	}
	if existing && (class.Class == ClassInvalid || class.Class == ClassUnknown) {
		msg := "The object stored today does not match the new CRD schema."
		if class.Field != "" {
			msg = "The object stored today does not match the new CRD schema at `" + class.Field + "`."
		}
		return &verdict.Finding{
			RuleID: ruleExisting, Severity: s.sev(verdict.SeverityBlocker, opt),
			Resource: objectID(u), Message: msg, Detail: class.Detail,
		}, false
	}
	switch class.Class {
	case ClassImmutable:
		field := class.Field
		if field == "" {
			field = "the field Kubernetes named"
		}
		return &verdict.Finding{
			RuleID: ruleImmutable, Severity: s.sev(verdict.SeverityBlocker, opt),
			Resource: objectID(u),
			Message:  "Kubernetes rejected the update. `" + field + "` is immutable.",
		}, false
	case ClassUnknown:
		sev := verdict.SeverityWarning
		detail := "A client-side Helm upgrade drops this field and continues."
		if opt.StrictUnknown {
			sev = verdict.SeverityBlocker
			detail = "Server-side apply rejects the upgrade."
		}
		if opt.Warnings {
			sev = verdict.SeverityWarning
		}
		field := class.Field
		msg := "Kubernetes does not know a field on " + objectID(u) + "."
		if field != "" {
			msg = "Kubernetes does not know `" + field + "`."
		}
		return &verdict.Finding{
			RuleID: ruleUnknown, Severity: sev, Resource: objectID(u), Message: msg, Detail: detail,
		}, false
	case ClassInvalid:
		msg := "Kubernetes rejected " + objectID(u) + "."
		if class.Field != "" {
			msg = "Kubernetes rejected `" + class.Field + "` on " + objectID(u) + "."
		}
		return &verdict.Finding{
			RuleID: ruleInvalid, Severity: s.sev(verdict.SeverityBlocker, opt),
			Resource: objectID(u), Message: msg, Detail: class.Detail,
		}, false
	default:
		f := notChecked(u, "the API server did not return a field error")
		return &f, true
	}
}

func (s *Server) sev(block verdict.Severity, opt ProofOptions) verdict.Severity {
	if opt.Warnings {
		return verdict.SeverityWarning
	}
	return block
}

func notChecked(u *unstructured.Unstructured, why string) verdict.Finding {
	return verdict.Finding{
		RuleID:   ruleNotSent,
		Severity: verdict.SeverityInfo,
		Resource: objectID(u),
		Message:  objectID(u) + " was not judged. " + why + ".",
	}
}

func (s *Server) apply(ctx context.Context, u *unstructured.Unstructured, dry bool) (*unstructured.Unstructured, error) {
	client, err := s.resourceClient(u)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(u.Object)
	if err != nil {
		return nil, err
	}
	force := true
	opts := metav1.PatchOptions{
		FieldManager:    fieldManager,
		Force:           &force,
		FieldValidation: metav1.FieldValidationStrict,
	}
	if dry {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	return client.Patch(ctx, u.GetName(), types.ApplyPatchType, data, opts)
}

func (s *Server) getObject(ctx context.Context, u *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	client, err := s.resourceClient(u)
	if err != nil {
		return nil, err
	}
	return client.Get(ctx, u.GetName(), metav1.GetOptions{})
}

func (s *Server) resourceClient(u *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	gvk := u.GroupVersionKind()
	mapping, err := s.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := u.GetNamespace()
		if ns == "" {
			ns = "default"
		}
		return s.dyn.Resource(mapping.Resource).Namespace(ns), nil
	}
	return s.dyn.Resource(mapping.Resource), nil
}

func (s *Server) ensureNamespace(ctx context.Context, name string) error {
	if name == "" || name == "default" || name == "kube-system" || name == "kube-public" || name == "kube-node-lease" {
		return nil
	}
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	_, err := s.dyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	ns := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": name},
	}}
	_, err = s.apply(ctx, ns, false)
	return err
}

func (s *Server) waitEstablished(ctx context.Context, name string) error {
	gvr := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	deadline := time.Now().Add(20 * time.Second)
	for {
		got, err := s.dyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		if err == nil && established(got) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func established(u *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprint(m["type"]) == "Established" && fmt.Sprint(m["status"]) == "True" {
			return true
		}
	}
	return false
}

func (s *Server) groupVersion(ctx context.Context, group, version string) (known, served bool, err error) {
	list, err := s.discovery.ServerGroups()
	if err != nil {
		return false, false, err
	}
	for _, g := range list.Groups {
		if g.Name != group {
			continue
		}
		known = true
		for _, v := range g.Versions {
			if v.Version == version {
				return true, true, nil
			}
		}
	}
	return known, false, nil
}

func (s *Server) refreshMapper() error {
	mapper, err := apiutil.NewDynamicRESTMapper(s.cfg, s.http)
	if err != nil {
		return err
	}
	s.mapper = mapper
	return nil
}

func docs(in []Manifest) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, m := range in {
		if len(m.Raw) == 0 {
			continue
		}
		b, err := yaml.Marshal(m.Raw)
		if err != nil {
			continue
		}
		u := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(b, &u.Object); err != nil || u.GetKind() == "" {
			continue
		}
		out = append(out, u)
	}
	return out
}

func filter(in []*unstructured.Unstructured, keep func(*unstructured.Unstructured) bool) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, u := range in {
		if keep(u) {
			out = append(out, u)
		}
	}
	return out
}

func splitCRDs(in []*unstructured.Unstructured) (crds, rest []*unstructured.Unstructured) {
	for _, u := range in {
		if u.GetKind() == "CustomResourceDefinition" && u.GroupVersionKind().Group == "apiextensions.k8s.io" {
			crds = append(crds, u)
			continue
		}
		rest = append(rest, u)
	}
	return crds, rest
}

func dedupe(in []*unstructured.Unstructured) []*unstructured.Unstructured {
	seen := map[string]bool{}
	var out []*unstructured.Unstructured
	for _, u := range in {
		if seen[u.GetName()] {
			continue
		}
		seen[u.GetName()] = true
		out = append(out, u)
	}
	return out
}

func specEqual(a, b *unstructured.Unstructured) bool {
	ab, _ := json.Marshal(a.Object["spec"])
	bb, _ := json.Marshal(b.Object["spec"])
	return bytes.Equal(ab, bb)
}

func crdGroupKinds(crds map[string]*unstructured.Unstructured) map[schema.GroupKind]bool {
	out := map[schema.GroupKind]bool{}
	for _, u := range crds {
		group, _, _ := unstructured.NestedString(u.Object, "spec", "group")
		kind, _, _ := unstructured.NestedString(u.Object, "spec", "names", "kind")
		if group != "" && kind != "" {
			out[schema.GroupKind{Group: group, Kind: kind}] = true
		}
	}
	return out
}

func skippedKind(kind string) bool {
	switch kind {
	case "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration", "APIService":
		return true
	default:
		return false
	}
}

func hook(u *unstructured.Unstructured) bool {
	_, ok := u.GetAnnotations()[hookAnnotation]
	return ok
}

func key(u *unstructured.Unstructured) string {
	gvk := u.GroupVersionKind()
	return gvk.Group + "/" + gvk.Version + "/" + gvk.Kind + "/" + u.GetNamespace() + "/" + u.GetName()
}

func objectID(u *unstructured.Unstructured) string {
	ns := u.GetNamespace()
	if ns == "" {
		ns = "default"
	}
	return fmt.Sprintf("%s/%s/%s", u.GetKind(), ns, u.GetName())
}
