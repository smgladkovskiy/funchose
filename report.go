package funchose

import (
	"log/slog"
	"slices"
)

// Report is the outcome of Up, Plan or Status.
type Report struct {
	Build int64
	// Table is the schema-qualified history table the run used.
	Table   string
	Changes []Change
}

// Applied returns the changes that modified, or would modify, a routine.
func (r Report) Applied() []Change { return r.filter(Action.Applies) }

// Warnings returns the changes that need attention.
func (r Report) Warnings() []Change { return r.filter(Action.Warns) }

// With returns the changes with any of the given actions.
func (r Report) With(actions ...Action) []Change {
	return r.filter(func(a Action) bool { return slices.Contains(actions, a) })
}

func (r Report) filter(keep func(Action) bool) []Change {
	var out []Change

	for _, c := range r.Changes {
		if keep(c.Action) {
			out = append(out, c)
		}
	}

	return out
}

// log writes every change that is not "unchanged" or "untracked", warnings at
// the warning level, and a summary line.
func (r Report) log(logger *slog.Logger) {
	if logger == nil {
		return
	}

	for _, change := range r.Changes {
		if change.Action == ActionUnchanged || change.Action == ActionUntracked {
			continue
		}

		attrs := []any{
			slog.String("routine", change.Routine()), slog.String("action", string(change.Action)),
			slog.String("from", short(change.From)), slog.Int64("from_build", change.FromBuild),
			slog.String("to", short(change.To)), slog.Int64("to_build", change.ToBuild),
			slog.Bool("changed", change.Changed),
		}
		if change.Note != "" {
			attrs = append(attrs, slog.String("note", change.Note))
		}

		if change.Action.Warns() {
			logger.Warn("funchose: routine needs attention", attrs...)

			continue
		}

		logger.Info("funchose: routine "+string(change.Action), attrs...)
	}

	logger.Info("funchose: done", slog.String("table", r.Table), slog.Int64("build", r.Build),
		slog.Int("applied", len(r.Applied())), slog.Int("warnings", len(r.Warnings())))
}

// shortSum is how much of a checksum is shown.
const shortSum = 12

// short shortens a checksum for display.
func short(sum string) string {
	if len(sum) > shortSum {
		return sum[:shortSum]
	}

	return sum
}
