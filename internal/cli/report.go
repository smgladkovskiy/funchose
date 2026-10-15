package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/smgladkovskiy/funchose"
)

// shortSum is how much of a checksum is shown.
const shortSum = 12

func printReport(out io.Writer, cmd string, rep funchose.Report, colors palette) {
	var unchanged, untracked, shown []funchose.Change

	for _, change := range rep.Changes {
		switch change.Action {
		case funchose.ActionUnchanged:
			unchanged = append(unchanged, change)
		case funchose.ActionUntracked:
			untracked = append(untracked, change)
		default:
			shown = append(shown, change)
		}
	}

	printHeader(out, cmd, rep, len(unchanged), colors)

	if len(shown) > 0 {
		fmt.Fprintln(out)
	}

	width := 0
	for _, change := range shown {
		width = max(width, len(change.Routine()))
	}

	for _, change := range shown {
		printChange(out, change, width, cmd != cmdStatus, colors)
	}

	printUntracked(out, cmd, untracked)
}

func printHeader(out io.Writer, cmd string, rep funchose.Report, unchanged int, colors palette) {
	verb := "to apply"
	if cmd == cmdUp {
		verb = "applied"
	}

	head := fmt.Sprintf("funchose %s: build %d on %s, %d %s, %d unchanged",
		cmd, rep.Build, rep.Table, len(rep.Applied()), verb, unchanged)
	if n := len(rep.Warnings()); n > 0 {
		head += ", " + colors.yellow(plural(n, "warning"))
	}

	fmt.Fprintln(out, head)
}

// printChange prints one change. measured is false for status: it does not
// run the files, so it cannot tell whether a definition would change.
func printChange(out io.Writer, change funchose.Change, width int, measured bool, colors palette) {
	action := fmt.Sprintf("%-9s", change.Action)

	switch {
	case change.Action.Warns():
		action = colors.yellow(action)
	case change.Action.Applies():
		action = colors.green(action)
	}

	fmt.Fprintf(out, "  %s %-*s  %s\n", action, width, change.Routine(), transition(change, measured))

	if change.Note != "" {
		fmt.Fprintf(out, "  %9s %-*s  %s\n", "", width, "", change.Note)
	}

	if change.Diff == "" {
		return
	}

	fmt.Fprintln(out)

	for line := range strings.SplitSeq(strings.TrimRight(change.Diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			line = colors.green(line)
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			line = colors.red(line)
		}

		fmt.Fprintln(out, "      "+line)
	}

	fmt.Fprintln(out)
}

func printUntracked(out io.Writer, cmd string, untracked []funchose.Change) {
	if len(untracked) == 0 {
		return
	}

	verbs := "have"
	if len(untracked) == 1 {
		verbs = "has"
	}

	fmt.Fprintf(out, "\n  %s in the managed schemas %s no file and no history", plural(len(untracked), "routine"), verbs)

	if cmd != cmdStatus {
		fmt.Fprintln(out, ": funchose status lists them")

		return
	}

	fmt.Fprintln(out, ":")

	for _, change := range untracked {
		fmt.Fprintf(out, "    %s\n", change.Routine())
	}
}

// transition prints "from (build N) -> to (build M)".
func transition(change funchose.Change, measured bool) string {
	side := func(sum string, build int64) string {
		if sum == "" {
			return "untracked"
		}

		return fmt.Sprintf("%s (build %d)", short(sum), build)
	}

	from, to := side(change.From, change.FromBuild), side(change.To, change.ToBuild)
	if change.Action == funchose.ActionCreate || change.Action == funchose.ActionDrop {
		from, to = strings.Replace(from, "untracked", "none", 1), strings.Replace(to, "untracked", "none", 1)
	}

	if from == to {
		return from
	}

	out := from + " -> " + to
	if measured && change.Action.Applies() && !change.Changed && change.Action != funchose.ActionDrop &&
		change.Diff == "" && change.Note == "" {
		out += ", definition unchanged"
	}

	return out
}

func short(sum string) string {
	if len(sum) > shortSum {
		return sum[:shortSum]
	}

	return sum
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}

	return strconv.Itoa(n) + " " + noun + "s"
}
