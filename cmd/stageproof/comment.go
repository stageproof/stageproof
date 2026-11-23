package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/stageproof/stageproof/internal/comment"
)

func runComment(ctx context.Context, args []string) int {
	return commentCommand(ctx, args, os.Stdout, os.Stderr, os.Getenv)
}

func commentCommand(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string) int {
	fs := flag.NewFlagSet("comment", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: stageproof comment --file <comment.md> [--provider auto|github|gitlab] [--pr N] [--mr IID] [--repo owner/name] [--marker text] [--best-effort]")
	}
	file := fs.String("file", "", "Markdown file from --comment-file")
	provider := fs.String("provider", "auto", "auto, github, or gitlab")
	pr := fs.String("pr", "", "GitHub pull request number. Default is the current GitHub Actions event")
	mr := fs.String("mr", "", "GitLab merge request iid. Default is CI_MERGE_REQUEST_IID")
	repo := fs.String("repo", "", "GitHub owner/name, or a GitLab project id or group/project path")
	marker := fs.String("marker", comment.DefaultMarker, "Text that identifies the comment this command updates")
	best := fs.Bool("best-effort", false, "Exit 0 when the server returns HTTP 403")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	return comment.Run(ctx, comment.Options{
		File:       *file,
		Provider:   *provider,
		PR:         *pr,
		MR:         *mr,
		Repo:       *repo,
		Marker:     *marker,
		BestEffort: *best,
		Env:        env,
		Stdout:     stdout,
		Stderr:     stderr,
	})
}
