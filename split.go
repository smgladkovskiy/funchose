package funchose

import "strings"

// statement is one top-level statement of a routine file.
type statement struct {
	head string // text with comments blanked out, enough to classify it
	text string // the statement exactly as written: what runs
	line int    // line the statement starts on
}

type splitError struct {
	line int
	msg  string
}

// routineWord is the position of FUNCTION in CREATE OR REPLACE FUNCTION.
const routineWord = 4

// split cuts SQL into top-level statements the way psql does: semicolons inside
// comments, quoted strings, quoted identifiers, dollar-quoted bodies and
// BEGIN ATOMIC ... END bodies do not end a statement.
func split(sql string) ([]statement, *splitError) {
	sc := scanner{sql: sql, line: 1}
	for sc.pos < len(sql) {
		if err := sc.next(); err != nil {
			return nil, err
		}
	}

	sc.flush(len(sql))

	return sc.stmts, nil
}

// scanner walks a routine file token by token.
type scanner struct {
	sql   string
	pos   int
	line  int
	stmts []statement

	cur      strings.Builder // the current statement with comments blanked out
	start    int             // line of the first token of cur, 0 while cur is blank
	from     int             // offset of the first token of cur
	depth    int             // BEGIN ATOMIC/CASE ... END nesting inside a CREATE FUNCTION
	words    int             // words seen in cur
	function bool            // cur starts with CREATE [OR REPLACE] FUNCTION|PROCEDURE
}

// next consumes one token.
func (sc *scanner) next() *splitError {
	char, rest := sc.sql[sc.pos], sc.sql[sc.pos:]

	switch {
	case char == '\n':
		sc.line++
		sc.keep(1)
	case isBlank(char):
		sc.keep(1)
	case strings.HasPrefix(rest, "--"):
		sc.lineComment()
	case strings.HasPrefix(rest, "/*"):
		return sc.blockComment()
	case isQuote(char):
		return sc.quoted(char)
	case char == '$' && !sc.afterWord():
		return sc.dollar()
	case char == ';' && sc.depth == 0:
		sc.keep(1)
		sc.flush(sc.pos)
	case identByte(char):
		sc.word()
	default:
		sc.mark()
		sc.keep(1)
	}

	return nil
}

// afterWord reports whether the byte before the current one belongs to a word:
// $ after a word is part of an identifier, not a dollar quote.
func (sc *scanner) afterWord() bool { return sc.pos > 0 && identByte(sc.sql[sc.pos-1]) }

// keep copies n bytes into the current statement.
func (sc *scanner) keep(n int) {
	sc.cur.WriteString(sc.sql[sc.pos : sc.pos+n])
	sc.pos += n
}

// mark notes where the current statement starts, at its first token.
func (sc *scanner) mark() {
	if sc.start == 0 {
		sc.start, sc.from = sc.line, sc.pos
	}
}

// flush ends the current statement at offset end.
func (sc *scanner) flush(end int) {
	if sc.start > 0 {
		sc.stmts = append(sc.stmts, statement{
			head: strings.TrimSpace(sc.cur.String()),
			text: strings.TrimSpace(sc.sql[sc.from:end]),
			line: sc.start,
		})
	}

	sc.cur.Reset()
	sc.start, sc.depth, sc.words, sc.function = 0, 0, 0, false
}

func (sc *scanner) lineComment() {
	end := strings.IndexByte(sc.sql[sc.pos:], '\n')
	if end < 0 {
		end = len(sc.sql) - sc.pos
	}

	sc.cur.WriteByte(' ')
	sc.pos += end
}

func (sc *scanner) blockComment() *splitError {
	at, nest := sc.line, 0

	for sc.pos < len(sc.sql) {
		switch rest := sc.sql[sc.pos:]; {
		case strings.HasPrefix(rest, "/*"):
			nest++
			sc.pos += 2
		case strings.HasPrefix(rest, "*/"):
			nest--
			sc.pos += 2
		default:
			if rest[0] == '\n' {
				sc.line++
				sc.cur.WriteByte('\n')
			}

			sc.pos++
		}

		if nest == 0 {
			sc.cur.WriteByte(' ')

			return nil
		}
	}

	return &splitError{at, "unterminated /* comment"}
}

// quoted consumes a quoted string or a quoted identifier.
func (sc *scanner) quoted(quote byte) *splitError {
	sc.mark()

	escapes := quote == '\'' && sc.pos > 0 && (sc.sql[sc.pos-1] == 'E' || sc.sql[sc.pos-1] == 'e') &&
		(sc.pos < 2 || !identByte(sc.sql[sc.pos-2]))
	at, end := sc.line, sc.pos+1

	for ; end < len(sc.sql); end++ {
		switch {
		case sc.sql[end] == '\n':
			sc.line++
		case escapes && sc.sql[end] == '\\':
			end++
		case sc.sql[end] == quote && end+1 < len(sc.sql) && sc.sql[end+1] == quote:
			end++
		case sc.sql[end] == quote:
			sc.keep(end + 1 - sc.pos)

			return nil
		}
	}

	return &splitError{at, "unterminated quoted string"}
}

// dollar consumes a dollar-quoted body, or a $ that does not start one.
func (sc *scanner) dollar() *splitError {
	sc.mark()

	tag := dollarTag(sc.sql[sc.pos:])
	if tag == "" {
		sc.keep(1)

		return nil
	}

	end := strings.Index(sc.sql[sc.pos+len(tag):], tag)
	if end < 0 {
		return &splitError{sc.line, "unterminated dollar-quoted body " + tag}
	}

	body := sc.sql[sc.pos : sc.pos+len(tag)+end+len(tag)]
	sc.line += strings.Count(body, "\n")
	sc.keep(len(body))

	return nil
}

// word consumes a word and follows the BEGIN ATOMIC ... END nesting of a
// SQL-standard routine body.
func (sc *scanner) word() {
	sc.mark()

	end := wordEnd(sc.sql, sc.pos)
	word := strings.ToLower(sc.sql[sc.pos:end])
	sc.words++

	if sc.words <= routineWord && (word == "function" || word == "procedure") &&
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(sc.cur.String())), "create") {
		sc.function = true
	}

	if sc.function {
		// BEGIN counts only before ATOMIC: begin is also a valid parameter or
		// column name.
		switch {
		case word == "begin" && nextWord(sc.sql[end:]) == "atomic":
			sc.depth++
		case word == "case" && sc.depth > 0:
			sc.depth++
		case word == "end" && sc.depth > 0:
			sc.depth--
		}
	}

	sc.keep(end - sc.pos)
}

// nextWord returns the next word of text in lower case, skipping blanks and
// comments.
func nextWord(text string) string {
	for {
		text = strings.TrimLeft(text, " \t\r\n\f")

		switch {
		case strings.HasPrefix(text, "--"):
			end := strings.IndexByte(text, '\n')
			if end < 0 {
				return ""
			}

			text = text[end:]
		case strings.HasPrefix(text, "/*"):
			end := strings.Index(text, "*/")
			if end < 0 {
				return ""
			}

			text = text[end+2:]
		default:
			return strings.ToLower(text[:wordEnd(text, 0)])
		}
	}
}

// wordEnd returns the offset just after the word that starts at pos.
func wordEnd(text string, pos int) int {
	for pos < len(text) && identByte(text[pos]) {
		pos++
	}

	return pos
}

// dollarTag returns the opening $tag$ at the start of text, or "" if text does
// not start a dollar quote ($1 is a parameter, not a quote).
func dollarTag(text string) string {
	end := wordEnd(text, 1)
	if end >= len(text) || text[end] != '$' {
		return ""
	}

	if end > 1 && text[1] >= '0' && text[1] <= '9' {
		return ""
	}

	return text[:end+1]
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\f' }

func isQuote(c byte) bool { return c == '\'' || c == '"' }

func identByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}
