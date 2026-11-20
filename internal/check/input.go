package check

import "time"

// Input is one comparison after flags, git, and Argo CD have been resolved.
type Input struct {
	BaseDir           string
	HeadDir           string
	BaseRef           string
	HeadRef           string
	BaseValues        []string
	HeadValues        []string
	ValueLabels       map[string]string
	SetValues         []string
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
}
