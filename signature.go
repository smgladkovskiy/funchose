package funchose

import (
	"regexp"
	"strings"
)

// signature returns the identity of a CREATE FUNCTION or PROCEDURE statement,
// "name(arguments) returns result" in lower case with normalized spacing, or ""
// for other statements. head is the statement with comments blanked out.
func signature(head string) string {
	match := createRe.FindStringSubmatchIndex(head)
	if match == nil {
		return ""
	}

	_, name := qualified(group(head, match, createFirst), group(head, match, createSecond))
	rest := head[match[1]:] // just after the opening parenthesis

	args := clause(rest, func(string) bool { return false })
	if len(args) == len(rest) {
		return "" // no closing parenthesis
	}

	sig := name + "(" + normalize(args) + ")"

	const returns = "returns"

	rest = strings.TrimLeft(rest[len(args)+1:], " \t\r\n\f")
	if len(rest) > len(returns) && strings.EqualFold(rest[:len(returns)], returns) && !identByte(rest[len(returns)]) {
		sig += " returns " + normalize(clause(rest[len(returns):], func(w string) bool { return routineOptions[w] }))
	}

	return sig
}

// routineOptions are the words that end the RETURNS clause of CREATE FUNCTION.
var routineOptions = map[string]bool{
	"language": true, "as": true, "immutable": true, "stable": true, "volatile": true,
	"strict": true, "called": true, "security": true, "external": true, "set": true,
	"cost": true, "rows": true, "parallel": true, "leakproof": true, "not": true,
	"window": true, "transform": true, "support": true, "begin": true, "return": true,
}

// clause returns text up to the first top-level word that stop accepts, or up
// to a closing parenthesis that has no opening one in text. Quoted strings,
// quoted identifiers, dollar quotes and parentheses are skipped as a whole.
func clause(text string, stop func(word string) bool) string {
	depth := 0

	for pos := 0; pos < len(text); {
		if next, ok := skipQuoted(text, pos); ok {
			if next < 0 {
				return text
			}

			pos = next

			continue
		}

		switch char := text[pos]; {
		case char == '(':
			depth++
			pos++
		case char == ')' && depth == 0:
			return text[:pos]
		case char == ')':
			depth--
			pos++
		case identByte(char):
			end := wordEnd(text, pos)
			if depth == 0 && stop(strings.ToLower(text[pos:end])) {
				return text[:pos]
			}

			pos = end
		default:
			pos++
		}
	}

	return text
}

// skipQuoted skips the quoted string, quoted identifier or dollar quote that
// starts at pos. It returns the offset after it, or -1 when it does not end,
// and false when nothing quoted starts at pos.
func skipQuoted(text string, pos int) (int, bool) {
	switch char := text[pos]; {
	case char == '\'' || char == '"':
		end := pos + 1
		for end < len(text) && (text[end] != char || end+1 < len(text) && text[end+1] == char) {
			if text[end] == char {
				end++ // a doubled quote
			}

			end++
		}

		return end + 1, true
	case char == '$' && (pos == 0 || !identByte(text[pos-1])):
		tag := dollarTag(text[pos:])
		if tag == "" {
			return 0, false
		}

		end := strings.Index(text[pos+len(tag):], tag)
		if end < 0 {
			return -1, true
		}

		return pos + len(tag) + end + len(tag), true
	default:
		return 0, false
	}
}

var (
	blanks     = regexp.MustCompile(`\s+`)
	punctBlank = regexp.MustCompile(`\s*([(),.\[\]])\s*`)
)

// normalize lowercases text and makes its spacing canonical.
func normalize(text string) string {
	text = strings.ToLower(strings.TrimSpace(blanks.ReplaceAllString(text, " ")))

	return punctBlank.ReplaceAllString(text, "$1")
}

// group returns submatch n of a FindStringSubmatchIndex result.
func group(text string, match []int, n int) string {
	if match[2*n] < 0 {
		return ""
	}

	return text[match[2*n]:match[2*n+1]]
}
