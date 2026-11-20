package check

import (
	"fmt"

	"github.com/stageproof/stageproof/internal/verdict"
)

func counted(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}

func countBlockers(findings []verdict.Finding) int {
	n := 0
	for _, f := range findings {
		if f.Severity == verdict.SeverityBlocker {
			n++
		}
	}
	return n
}

func labelPaths(paths []string, labels map[string]string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		if label, ok := labels[p]; ok && label != "" {
			out[i] = label
			continue
		}
		out[i] = p
	}
	return out
}
