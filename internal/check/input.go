package check

import "time"

// Input is one comparison after flags, git, and Argo CD have been resolved.
type Input struct {
	// Adapter is the tool that produced the objects: helm, argocd, or manifests.
	// Empty means helm, so existing callers stay on the chart path.
	Adapter       string
	ArgoSkipCrds  *bool
	FluxCRDPolicy string
	// ValueGaps are valuesFrom references that were not read. The chart is
	// still rendered. The result is not PROVEN.
	ValueGaps []string
	// ManifestMode reads rendered YAML from ManifestBefore and ManifestAfter.
	// Those paths are files or directories, not Helm charts.
	ManifestMode   bool
	ManifestBefore string
	ManifestAfter  string

	BaseDir     string
	HeadDir     string
	BaseRef     string
	HeadRef     string
	BaseValues  []string
	HeadValues  []string
	ValueLabels map[string]string
	SetValues   []string
	// Per-side Helm sets come from an Argo CD Application. The shared
	// SetValues above are the command's own --set flags and apply to both.
	BaseSet           []string
	HeadSet           []string
	BaseSetString     []string
	HeadSetString     []string
	BaseSetFile       []string
	HeadSetFile       []string
	BaseRelease       string
	HeadRelease       string
	BaseNamespace     string
	HeadNamespace     string
	BaseSkipCRDs      bool
	HeadSkipCRDs      bool
	KubeVersion       string
	NextKubernetes    string
	APIServer         string // auto, off, or require. Empty skips the API server so tests stay offline.
	APIServerDir      string
	ServerSideApply   bool
	RequireRehearsal  bool
	Live              bool
	ClusterName       string
	KeepCluster       bool
	Timeout           time.Duration
	HelmTest          bool
	CRDFiles          []string
	PlaceholderTokens []string
	Started           time.Time
	// ValuesFull render-tests every user key and every changed chart default.
	// The default proves keys the template reader drops or cannot decide.
	ValuesFull bool
}
