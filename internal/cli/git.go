package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// git runs a git command and returns its non-empty output lines.
func git(ctx context.Context, args ...string) ([]string, error) {
	out, err := gitOutput(ctx, args...)
	if err != nil {
		return nil, err
	}

	var lines []string

	for line := range strings.SplitSeq(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}

	return lines, nil
}

// gitOutput runs a git command and returns its output as is.
func gitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", errGit, msg)
		}

		return "", fmt.Errorf("%w: %w", errGit, err)
	}

	return string(out), nil
}

// targetRef resolves the target branch from the flag or the merge request
// variables of GitLab CI and GitHub Actions; "" outside a merge request.
func targetRef(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}

	for _, env := range []string{"CI_MERGE_REQUEST_TARGET_BRANCH_NAME", "GITHUB_BASE_REF"} {
		if name := os.Getenv(env); name != "" {
			return "origin/" + name
		}
	}

	return ""
}

// forkPoint resolves the commit the branch forked from.
func forkPoint(ctx context.Context, flagValue, base, head string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}

	if sha := os.Getenv("CI_MERGE_REQUEST_DIFF_BASE_SHA"); sha != "" {
		return sha, nil
	}

	if head == "" {
		head = "HEAD"
	}

	lines, err := git(ctx, "merge-base", base, head)
	if err != nil || len(lines) == 0 {
		return "", fmt.Errorf("%w: cannot find where HEAD forked from %s (shallow clone?): "+
			"fetch more history or pass -since <commit>", errGit, base)
	}

	return lines[0], nil
}

// branch is what a branch did to the routine files since it forked.
type branch struct {
	fork, head string   // head "" is the working tree
	changed    []string // files added or modified, relative to the working directory
}

// readBranch lists the routine files the branch added or modified since fork. A
// moved file is a new one: that is what it means for the routines.
func readBranch(ctx context.Context, dir, fork, head string) (branch, error) {
	args := []string{"diff", "--name-only", "--relative", "--no-renames", "--diff-filter=ACMR", fork}
	if head != "" {
		args = append(args, head)
	}

	changes := branch{fork: fork, head: head}

	var err error
	if changes.changed, err = git(ctx, append(args, "--", dir)...); err != nil {
		return changes, fmt.Errorf("cannot diff against the fork point %s: %w (in CI: git fetch --depth=1 origin %s)",
			fork, err, fork)
	}

	if head == "" {
		added, err := git(ctx, "ls-files", "--others", "--exclude-standard", "--", dir)
		if err != nil {
			return changes, err
		}

		changes.changed = append(changes.changed, added...)
	}

	slices.Sort(changes.changed)

	return changes, nil
}

// file returns the content of a file in the branch: at head, or in the working tree.
func (b branch) file(ctx context.Context, path string) (string, error) {
	if b.head == "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}

		return string(data), nil
	}

	return gitOutput(ctx, "show", b.head+":./"+filepath.ToSlash(path))
}
