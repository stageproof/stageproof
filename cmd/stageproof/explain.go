package main

import (
	"fmt"
	"io"
	"os"

	"github.com/stageproof/stageproof/internal/rules"
)

func runExplain(args []string) int {
	return explainTo(os.Stdout, os.Stderr, args)
}

func explainTo(w, errw io.Writer, args []string) int {
	if len(args) != 1 || args[0] == "" || args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprintln(errw, "usage: stageproof explain <rule-id>")
		if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
			return 0
		}
		return 2
	}
	text, ok := rules.Explain(args[0])
	if !ok {
		_, _ = fmt.Fprintf(errw, "error: no rule %q\n", args[0])
		return 2
	}
	_, _ = fmt.Fprint(w, text)
	return 0
}
