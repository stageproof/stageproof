package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/stageproof/stageproof/internal/mcpserver"
)

func runMCP(ctx context.Context, args []string) int {
	return mcpCommand(ctx, args, os.Stderr)
}

func mcpCommand(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "directory tools may read")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "stageproof mcp [--root <dir>]")
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	err := mcpserver.Run(ctx, *root)
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "stageproof: %v\n", err)
	return 2
}
