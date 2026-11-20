package check

import (
	"time"

	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
)

// State is what the stages pass along. The report is the only part that is printed.
type State struct {
	Report           *verdict.Report
	BaseObjs         []k8sschema.ManifestObject
	HeadObjs         []k8sschema.ManifestObject
	Placeholders     []string
	Started          time.Time
	Require          bool
	Stopped          bool
	APIServerRan     bool
	APIServerVersion string
	NextAPIServerRan bool
	StoredBefore     [][]byte
	StoredAfter      [][]byte
}

func (s *State) stop(runtimeOK, couldNotCheck bool) {
	s.Report.DurationMs = time.Since(s.Started).Milliseconds()
	s.Report.Finalize(runtimeOK, couldNotCheck, s.Require)
	s.Stopped = true
}
