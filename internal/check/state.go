package check

import (
	"time"

	"github.com/stageproof/stageproof/internal/apiserver"
	"github.com/stageproof/stageproof/internal/config"
	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
)

// State is what the stages pass along. The report is the only part that is printed.
type State struct {
	Report       *verdict.Report
	BaseObjs     []k8sschema.ManifestObject
	HeadObjs     []k8sschema.ManifestObject
	Placeholders []string
	// ServedAPIs is the Kubernetes discovery list (group/version and
	// group/version/Kind) passed to helm template via --api-versions.
	ServedAPIs []string
	// RepairSets, RepairAPIs, and RepairSkipSchema are discovered while
	// rendering. Later stages must use them or they render a different chart
	// than the one the findings describe.
	RepairSets        []string
	RepairJSON        []string
	RepairAPIs        []string
	RepairSkipSchema  bool
	Started           time.Time
	Require           bool
	Stopped           bool
	APIServerRan      bool
	APIServerVersion  string
	NextAPIServerRan  bool
	StoredBefore      [][]byte
	StoredAfter       [][]byte
	Config            *config.Config
	apiServer         *apiserver.Server
	apiServerPrepared bool
	apiServerStartErr error
}

func (s *State) closeAPIServer() {
	if s == nil || s.apiServer == nil {
		return
	}
	_ = s.apiServer.Stop()
	s.apiServer = nil
}

func (s *State) stop(runtimeOK, couldNotCheck bool) {
	if s.Config != nil && s.Report != nil {
		config.Apply(s.Config, s.Report, time.Now().UTC())
	}
	s.Report.DurationMs = time.Since(s.Started).Milliseconds()
	s.Report.Finalize(runtimeOK, couldNotCheck, s.Require)
	s.Stopped = true
}
