package funchose

import "fmt"

// Problem is a routine file that funchose refuses to apply.
type Problem struct {
	Path string
	Line int // 0 when the problem is about the file as a whole
	Msg  string
}

// String returns path:line: message, the way compilers report.
func (p Problem) String() string {
	if p.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", p.Path, p.Line, p.Msg)
	}

	return fmt.Sprintf("%s: %s", p.Path, p.Msg)
}
