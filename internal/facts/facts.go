// Package facts reads a cluster the caller names and writes the versions,
// custom resource definitions, and installed chart identities Stageproof
// checks need. It does not read Secret values and it does not write to the
// cluster.
package facts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	schemaVersion = 1
	sourceHelm    = "helm-release"
	sourceArgo    = "argocd"
	sourceFlux    = "flux"
	argoGroup     = "argoproj.io"
	argoResource  = "applications"
	fluxGroup     = "helm.toolkit.fluxcd.io"
	fluxResource  = "helmreleases"
)

// Document is the JSON written by `stageproof facts export`.
type Document struct {
	SchemaVersion                     int      `json:"schema_version"`
	ServerVersion                     string   `json:"server_version"`
	Nodes                             []Node   `json:"nodes"`
	CRDs                              []CRD    `json:"crds"`
	ValidatingAdmissionPolicies       []Policy `json:"validating_admission_policies"`
	ValidatingAdmissionPolicyBindings []Policy `json:"validating_admission_policy_bindings"`
	StorageClasses                    []string `json:"storage_classes"`
	Charts                            []Chart  `json:"charts"`
}

// Node is one node's operating system and architecture.
type Node struct {
	Name            string `json:"name"`
	Architecture    string `json:"architecture"`
	OperatingSystem string `json:"operating_system"`
}

// CRD is one custom resource definition. Spec is the API spec. StoredVersions
// is status.storedVersions.
type CRD struct {
	Name           string          `json:"name"`
	ServedVersions []string        `json:"served_versions"`
	StorageVersion string          `json:"storage_version"`
	StoredVersions []string        `json:"stored_versions"`
	Spec           json.RawMessage `json:"spec"`
}

// Policy is a validating admission policy or a binding. Spec is the API spec.
type Policy struct {
	Name string          `json:"name"`
	Spec json.RawMessage `json:"spec"`
}

// Chart is an installed chart identity. It has no values.
type Chart struct {
	Source     string `json:"source"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	AppVersion string `json:"app_version,omitempty"`
	Release    string `json:"release,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
}

// ExportOptions selects the reads that are off unless the caller asks.
type ExportOptions struct {
	// HelmReleases lists Secrets of type helm.sh/release.v1 and keeps the
	// chart name, version, appVersion, release name, and namespace.
	HelmReleases bool
}

// Export lists the cluster named by cfg. cfg must already be limited to the
// shipped ClusterRole. This function only calls get and list.
func Export(ctx context.Context, cfg *rest.Config, opt ExportOptions) (*Document, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg == nil {
		return nil, fmt.Errorf("kubeconfig is empty")
	}
	cfg = rest.CopyConfig(cfg)
	if cfg.QPS == 0 {
		cfg.QPS = 50
	}
	if cfg.Burst == 0 {
		cfg.Burst = 100
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	ext, err := apiextensions.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	info, err := disc.ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("server version: %w", err)
	}
	doc := emptyDocument(strings.TrimPrefix(strings.TrimSpace(info.GitVersion), "v"))

	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	for _, node := range nodes.Items {
		doc.Nodes = append(doc.Nodes, Node{
			Name:            node.Name,
			Architecture:    node.Status.NodeInfo.Architecture,
			OperatingSystem: node.Status.NodeInfo.OperatingSystem,
		})
	}
	sort.Slice(doc.Nodes, func(i, j int) bool { return doc.Nodes[i].Name < doc.Nodes[j].Name })

	crds, err := listCRDs(ctx, ext)
	if err != nil {
		return nil, err
	}
	doc.CRDs = crds

	policies, err := listPolicies(ctx, cs)
	if err != nil {
		return nil, err
	}
	doc.ValidatingAdmissionPolicies = policies

	bindings, err := listPolicyBindings(ctx, cs)
	if err != nil {
		return nil, err
	}
	doc.ValidatingAdmissionPolicyBindings = bindings

	classes, err := cs.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil && !missingAPI(err) {
		return nil, fmt.Errorf("list storage classes: %w", err)
	}
	if classes != nil {
		for _, sc := range classes.Items {
			if sc.Name != "" {
				doc.StorageClasses = append(doc.StorageClasses, sc.Name)
			}
		}
	}
	sort.Strings(doc.StorageClasses)

	groups, err := disc.ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("discover api groups: %w", err)
	}
	apps, err := listGroup(ctx, dyn, groups, argoGroup, argoResource)
	if err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}
	for _, item := range apps {
		doc.Charts = append(doc.Charts, chartsFromApplication(item)...)
	}
	releases, err := listGroup(ctx, dyn, groups, fluxGroup, fluxResource)
	if err != nil {
		return nil, fmt.Errorf("list helm releases: %w", err)
	}
	for _, item := range releases {
		doc.Charts = append(doc.Charts, chartsFromHelmRelease(item)...)
	}
	if opt.HelmReleases {
		secrets, err := cs.CoreV1().Secrets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list secrets: %w", err)
		}
		helmCharts, err := chartsFromHelmSecrets(secrets.Items)
		if err != nil {
			return nil, err
		}
		doc.Charts = append(doc.Charts, helmCharts...)
	}
	sortCharts(doc.Charts)
	return doc, nil
}

func emptyDocument(version string) *Document {
	if i := strings.Index(version, "+"); i >= 0 {
		version = version[:i]
	}
	return &Document{
		SchemaVersion:                     schemaVersion,
		ServerVersion:                     version,
		Nodes:                             []Node{},
		CRDs:                              []CRD{},
		ValidatingAdmissionPolicies:       []Policy{},
		ValidatingAdmissionPolicyBindings: []Policy{},
		StorageClasses:                    []string{},
		Charts:                            []Chart{},
	}
}

func listCRDs(ctx context.Context, ext apiextensions.Interface) ([]CRD, error) {
	list, err := ext.ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
	if err != nil {
		if missingAPI(err) {
			return []CRD{}, nil
		}
		return nil, fmt.Errorf("list custom resource definitions: %w", err)
	}
	out := make([]CRD, 0, len(list.Items))
	for _, item := range list.Items {
		one, err := crdFromAPI(item)
		if err != nil {
			return nil, err
		}
		out = append(out, one)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func crdFromAPI(item apiextensionsv1.CustomResourceDefinition) (CRD, error) {
	spec, err := json.Marshal(item.Spec)
	if err != nil {
		return CRD{}, fmt.Errorf("encode custom resource definition %s: %w", item.Name, err)
	}
	served := make([]string, 0, len(item.Spec.Versions))
	storage := ""
	for _, v := range item.Spec.Versions {
		if v.Served && v.Name != "" {
			served = append(served, v.Name)
		}
		if v.Storage && storage == "" {
			storage = v.Name
		}
	}
	sort.Strings(served)
	stored := append([]string(nil), item.Status.StoredVersions...)
	sort.Strings(stored)
	return CRD{
		Name:           item.Name,
		ServedVersions: served,
		StorageVersion: storage,
		StoredVersions: stored,
		Spec:           spec,
	}, nil
}

func listPolicies(ctx context.Context, cs kubernetes.Interface) ([]Policy, error) {
	list, err := cs.AdmissionregistrationV1().ValidatingAdmissionPolicies().List(ctx, metav1.ListOptions{})
	if err != nil {
		if missingAPI(err) {
			return []Policy{}, nil
		}
		return nil, fmt.Errorf("list validating admission policies: %w", err)
	}
	return policiesFrom(list.Items, func(item admissionregistrationv1.ValidatingAdmissionPolicy) (string, any) {
		return item.Name, item.Spec
	})
}

func listPolicyBindings(ctx context.Context, cs kubernetes.Interface) ([]Policy, error) {
	list, err := cs.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		if missingAPI(err) {
			return []Policy{}, nil
		}
		return nil, fmt.Errorf("list validating admission policy bindings: %w", err)
	}
	return policiesFrom(list.Items, func(item admissionregistrationv1.ValidatingAdmissionPolicyBinding) (string, any) {
		return item.Name, item.Spec
	})
}

func policiesFrom[T any](items []T, spec func(T) (string, any)) ([]Policy, error) {
	out := make([]Policy, 0, len(items))
	for _, item := range items {
		name, body := spec(item)
		if name == "" {
			continue
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", name, err)
		}
		out = append(out, Policy{Name: name, Spec: raw})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Load reads a facts file. The check does not look for this file in git.
func Load(path string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf("%s schema_version must be %d", path, schemaVersion)
	}
	if doc.Nodes == nil {
		doc.Nodes = []Node{}
	}
	if doc.CRDs == nil {
		doc.CRDs = []CRD{}
	}
	if doc.ValidatingAdmissionPolicies == nil {
		doc.ValidatingAdmissionPolicies = []Policy{}
	}
	if doc.ValidatingAdmissionPolicyBindings == nil {
		doc.ValidatingAdmissionPolicyBindings = []Policy{}
	}
	if doc.StorageClasses == nil {
		doc.StorageClasses = []string{}
	}
	if doc.Charts == nil {
		doc.Charts = []Chart{}
	}
	if _, err := doc.CRDObjects(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &doc, nil
}

// Write stores doc as indented JSON. Slices are sorted first.
func Write(path string, doc *Document) error {
	if doc == nil {
		return fmt.Errorf("facts document is empty")
	}
	doc.SchemaVersion = schemaVersion
	sort.Slice(doc.Nodes, func(i, j int) bool { return doc.Nodes[i].Name < doc.Nodes[j].Name })
	sort.Slice(doc.CRDs, func(i, j int) bool { return doc.CRDs[i].Name < doc.CRDs[j].Name })
	sort.Slice(doc.ValidatingAdmissionPolicies, func(i, j int) bool {
		return doc.ValidatingAdmissionPolicies[i].Name < doc.ValidatingAdmissionPolicies[j].Name
	})
	sort.Slice(doc.ValidatingAdmissionPolicyBindings, func(i, j int) bool {
		return doc.ValidatingAdmissionPolicyBindings[i].Name < doc.ValidatingAdmissionPolicyBindings[j].Name
	})
	sort.Strings(doc.StorageClasses)
	sortCharts(doc.Charts)
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

// KubernetesMinor is the major.minor of the server version, such as 1.35.
// The check uses it only when --current-k8s was not set.
func (d *Document) KubernetesMinor() string {
	if d == nil {
		return ""
	}
	v := strings.TrimPrefix(strings.TrimSpace(d.ServerVersion), "v")
	if i := strings.IndexAny(v, "+-"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "." + parts[1]
}

// Platforms is linux/amd64 style pairs from the nodes. Empty means the check
// keeps its default of linux/amd64. A node without an operating system or an
// architecture is skipped.
func (d *Document) Platforms() []string {
	if d == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, node := range d.Nodes {
		if node.OperatingSystem == "" || node.Architecture == "" {
			continue
		}
		platform := node.OperatingSystem + "/" + node.Architecture
		if seen[platform] {
			continue
		}
		seen[platform] = true
		out = append(out, platform)
	}
	sort.Strings(out)
	return out
}

// StoredVersions maps a custom resource definition name to the versions the
// cluster still has stored.
func (d *Document) StoredVersions() map[string][]string {
	if d == nil {
		return nil
	}
	out := map[string][]string{}
	for _, crd := range d.CRDs {
		if crd.Name == "" || len(crd.StoredVersions) == 0 {
			continue
		}
		cp := append([]string(nil), crd.StoredVersions...)
		sort.Strings(cp)
		out[crd.Name] = cp
	}
	return out
}

// CRDObjects rebuilds each custom resource definition so the local API server
// can install it before the chart's own definitions. The object is the name
// and the spec. It is not applied to the cluster the facts came from.
func (d *Document) CRDObjects() ([]map[string]any, error) {
	if d == nil {
		return nil, nil
	}
	names := make([]string, 0, len(d.CRDs))
	byName := map[string]CRD{}
	for _, crd := range d.CRDs {
		if crd.Name == "" || len(bytes.TrimSpace(crd.Spec)) == 0 || bytes.Equal(bytes.TrimSpace(crd.Spec), []byte("null")) {
			continue
		}
		if _, ok := byName[crd.Name]; ok {
			continue
		}
		byName[crd.Name] = crd
		names = append(names, crd.Name)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		var spec any
		if err := json.Unmarshal(byName[name].Spec, &spec); err != nil {
			return nil, fmt.Errorf("custom resource definition %s: %w", name, err)
		}
		if _, ok := spec.(map[string]any); !ok {
			return nil, fmt.Errorf("custom resource definition %s spec is not an object", name)
		}
		out = append(out, map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata":   map[string]any{"name": name},
			"spec":       spec,
		})
	}
	return out, nil
}

func sortCharts(charts []Chart) {
	sort.Slice(charts, func(i, j int) bool {
		a, b := charts[i], charts[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Release != b.Release {
			return a.Release < b.Release
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Version < b.Version
	})
}

func missingAPI(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return true
	}
	return strings.Contains(err.Error(), "the server could not find the requested resource")
}

func listGroup(ctx context.Context, dyn dynamic.Interface, groups *metav1.APIGroupList, group, resource string) ([]map[string]any, error) {
	if groups == nil {
		return nil, nil
	}
	var versions []string
	for _, g := range groups.Groups {
		if g.Name != group {
			continue
		}
		if g.PreferredVersion.Version != "" {
			versions = append(versions, g.PreferredVersion.Version)
		}
		for _, v := range g.Versions {
			if v.Version != "" && v.Version != g.PreferredVersion.Version {
				versions = append(versions, v.Version)
			}
		}
	}
	if len(versions) == 0 {
		return nil, nil
	}
	for _, ver := range versions {
		gvr := schema.GroupVersionResource{Group: group, Version: ver, Resource: resource}
		list, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
		if missingAPI(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(list.Items))
		for _, item := range list.Items {
			out = append(out, item.Object)
		}
		return out, nil
	}
	return nil, nil
}
