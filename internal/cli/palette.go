package cli

import (
	"fmt"
	"io"
	"os"
)

// palette paints output in ANSI colors when they are on.
type palette struct{ on bool }

func newPalette(on bool) palette { return palette{on: on} }

func (p palette) red(a ...any) string    { return p.paint("1;31", a...) }
func (p palette) green(a ...any) string  { return p.paint("32", a...) }
func (p palette) yellow(a ...any) string { return p.paint("33", a...) }
func (p palette) bold(a ...any) string   { return p.paint("1", a...) }

func (p palette) paint(code string, a ...any) string {
	if !p.on {
		return fmt.Sprint(a...)
	}

	return "\x1b[" + code + "m" + fmt.Sprint(a...) + "\x1b[0m"
}

// colorEnabled follows the NO_COLOR and FORCE_COLOR conventions. CI logs of
// GitLab and GitHub render ANSI colors, so CI counts as a color terminal.
func colorEnabled(out io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}

	if os.Getenv("FORCE_COLOR") != "" || os.Getenv("CI") != "" {
		return true
	}

	file, ok := out.(*os.File)
	if !ok {
		return false
	}

	st, err := file.Stat()

	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
