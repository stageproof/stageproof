package helm

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

var (
	majorOnce sync.Once
	majorVal  int
)

// Major is the major version of the helm binary on PATH.
// It is 0 when helm cannot be run.
func Major(ctx context.Context) int {
	majorOnce.Do(func() {
		majorVal = detectMajor(ctx)
	})
	return majorVal
}

func detectMajor(ctx context.Context) int {
	cmd := exec.CommandContext(ctx, "helm", "version", "--template", "{{.Version}}")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	text := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	text = strings.TrimPrefix(text, "V")
	head, _, _ := strings.Cut(text, ".")
	n, err := strconv.Atoi(head)
	if err != nil {
		return 0
	}
	return n
}
