package report

import (
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/verdict"
)

// OrderFindings puts deletes, CRD breaks, privilege, exposure, values,
// restarts, and capacity ahead of everything else. The order is stable.
func OrderFindings(in []verdict.Finding) []verdict.Finding {
	if len(in) < 2 {
		return in
	}
	type ranked struct {
		i int
		f verdict.Finding
	}
	rows := make([]ranked, len(in))
	for i, f := range in {
		rows[i] = ranked{i: i, f: f}
	}
	sort.SliceStable(rows, func(a, b int) bool {
		ca, cb := findingCategory(rows[a].f.RuleID), findingCategory(rows[b].f.RuleID)
		if ca != cb {
			return ca < cb
		}
		return rows[a].i < rows[b].i
	})
	out := make([]verdict.Finding, len(rows))
	for i, row := range rows {
		out[i] = row.f
	}
	return out
}

func findingCategory(id string) int {
	switch {
	case id == "impact.delete" || strings.HasPrefix(id, "impact.delete."):
		return 0
	case strings.HasPrefix(id, "crd."):
		return 1
	case strings.HasPrefix(id, "impact.privilege."):
		return 2
	case strings.HasPrefix(id, "impact.exposure."):
		return 3
	case strings.HasPrefix(id, "helm.values."):
		return 4
	case id == "impact.restart" || strings.HasPrefix(id, "impact.restart."):
		return 5
	case strings.HasPrefix(id, "impact.capacity."):
		return 6
	default:
		return 7
	}
}
