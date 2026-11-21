package helm

import (
	"context"
	"os/exec"
	"strings"

	"github.com/stageproof/stageproof/internal/k8sdiff"
	"github.com/stageproof/stageproof/internal/k8sschema"
)

// RenderSide is one helm binary in a comparison.
type RenderSide struct {
	Bin     string
	Version string
}

// RenderDiff is the parsed difference between two helm binaries.
// Failed is set when a binary exits non-zero or its render is not YAML.
// Effects name objects and paths. Values are not for display.
type RenderDiff struct {
	Left    RenderSide
	Right   RenderSide
	Effects []k8sdiff.Effect
	Failed  bool
	Failure string
}

// BinaryVersion runs `helm version --template {{.Version}}`.
// An empty string means the binary did not answer.
func BinaryVersion(ctx context.Context, bin string) string {
	if bin == "" {
		bin = "helm"
	}
	cmd := exec.CommandContext(ctx, bin, "version", "--template", "{{.Version}}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CompareRenders renders the chart twice with each binary and diffs the
// parsed objects. A field that changes between the two renders of one binary
// is masked, the same way a values proof ignores a volatile field.
func CompareRenders(ctx context.Context, leftBin, rightBin string, opts RenderOptions, secrets []string) RenderDiff {
	if leftBin == "" {
		leftBin = "helm"
	}
	diff := RenderDiff{
		Left:  RenderSide{Bin: leftBin, Version: BinaryVersion(ctx, leftBin)},
		Right: RenderSide{Bin: rightBin, Version: BinaryVersion(ctx, rightBin)},
	}
	leftA, leftB, ok := twice(ctx, leftBin, opts, secrets, &diff)
	if !ok {
		return diff
	}
	rightA, rightB, ok := twice(ctx, rightBin, opts, secrets, &diff)
	if !ok {
		return diff
	}
	mask := k8sdiff.MaskOf(leftA, leftB)
	for k, v := range k8sdiff.MaskOf(rightA, rightB) {
		if v {
			mask[k] = true
		}
	}
	diff.Effects = k8sdiff.Diff(leftA, rightA, mask)
	return diff
}

func twice(ctx context.Context, bin string, opts RenderOptions, secrets []string, diff *RenderDiff) ([]k8sschema.ManifestObject, []k8sschema.ManifestObject, bool) {
	first, err := RenderChartBinary(ctx, bin, opts)
	if err != nil {
		fail(diff, err, secrets)
		return nil, nil, false
	}
	second, err := RenderChartBinary(ctx, bin, opts)
	if err != nil {
		fail(diff, err, secrets)
		return nil, nil, false
	}
	a, err := k8sschema.ParseManifestStream(first)
	if err != nil {
		fail(diff, err, secrets)
		return nil, nil, false
	}
	b, err := k8sschema.ParseManifestStream(second)
	if err != nil {
		fail(diff, err, secrets)
		return nil, nil, false
	}
	return a, b, true
}

func fail(diff *RenderDiff, err error, secrets []string) {
	diff.Failed = true
	diff.Failure = oneLine(scrubSecrets(err.Error(), secrets))
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 240 {
		s = s[:240]
	}
	return s
}
