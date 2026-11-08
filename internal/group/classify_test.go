package group

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/Vaspyyy/seek/internal/search"
)

// The reference: what classify asked before lineText, for the text before
// one match, read from the start of the line each time.

// refInString: the text ends inside a string literal. Interpolations
// (`${x}` in a template, {x} in a Python f-string) are code, and a
// character literal ('"') opens nothing.
func refInString(before string, st style) bool {
	var open byte
	interp := false // the open string interpolates: ` or f"
	depth := 0      // inside ${…} or {…} of an interpolating string
	for i := 0; i < len(before); i++ {
		c := before[i]
		if depth > 0 {
			switch c {
			case '{':
				depth++
			case '}':
				depth--
			}
			continue
		}
		if c == '\\' {
			i++
			continue
		}
		if open != 0 {
			switch {
			case c == open:
				open = 0
			case interp && open == '`' && c == '$' && i+1 < len(before) && before[i+1] == '{':
				depth, i = 1, i+1
			case interp && open != '`' && c == '{':
				if i+1 < len(before) && before[i+1] == '{' {
					i++ // {{ is a literal brace
				} else {
					depth = 1
				}
			}
			continue
		}
		if c == '/' && st.js && i+1 < len(before) && before[i+1] != '/' && before[i+1] != '*' && regexStart(before[:i]) {
			end := regexEnd(before, i)
			if end < 0 {
				return true // the match is inside the regex literal
			}
			i = end - 1
			continue
		}
		if c == '\'' && strings.IndexByte(st.quotes, '\'') < 0 {
			// a character literal: 'x', '\n', '"' (Rust lifetimes have no closing quote)
			if i+2 < len(before) && before[i+2] == '\'' {
				i += 2
			} else if i+3 < len(before) && before[i+1] == '\\' && before[i+3] == '\'' {
				i += 3
			}
			continue
		}
		if strings.IndexByte(st.quotes, c) >= 0 {
			if c == '\'' && i > 0 && isWord(before[i-1]) && !fPrefix(before[:i]) {
				continue // it's, don't
			}
			open = c
			interp = c == '`' || st.fstrings && fPrefix(before[:i])
		}
	}
	return open != 0 && depth == 0
}

// refInComment: the text before a match ends in a comment.
func refInComment(before, trimmed string, st style) bool {
	for _, m := range st.line {
		if strings.HasPrefix(trimmed, m) {
			return true
		}
		// a marker earlier on the line, outside a string
		for i := strings.Index(before, m); i >= 0; {
			if !refInString(before[:i], st) && (m != "#" || i == 0 || before[i-1] == ' ' || before[i-1] == '\t') {
				return true
			}
			j := strings.Index(before[i+len(m):], m)
			if j < 0 {
				break
			}
			i += len(m) + j
		}
	}
	if st.block {
		if strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "* ") || trimmed == "*" || strings.HasPrefix(trimmed, "*/") {
			return true
		}
		if i := strings.LastIndex(before, "/*"); i >= 0 && !strings.Contains(before[i:], "*/") && !refInString(before[:i], st) {
			return true
		}
	}
	if st.line == nil && !st.block && strings.Contains(before, "<!--") && !strings.Contains(before, "-->") {
		return true
	}
	return false
}

func refClassify(h search.Hit, st style) string {
	if len(h.Spans) == 0 {
		return ""
	}
	trimmed := strings.TrimSpace(h.Text)
	first := ""
	for k, sp := range h.Spans {
		before := h.Text[:sp[0]]
		l := ""
		switch {
		case refInComment(before, trimmed, st):
			l = "comment"
		case refInString(before, st):
			l = "string"
		}
		if l == "" {
			return classifyCode(h, sp, trimmed, st)
		}
		if k == 0 {
			first = l
		}
	}
	return first
}

var testStyles = map[string]style{"js": jsStyle, "slash": slashStyle, "typed": typedStyle, "hash": hashStyle, "py": pyStyle,
	"conf": confStyle, "dash": dashStyle, "css": cssStyle, "plain": plainStyle, "jsonc": styleOf("a.jsonc")}

// lineText answers what the reference answers, at every byte of random
// lines made of the characters that matter.
func TestLineTextMatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	const alphabet = "ab f r_( )=,{}$`'\"/\\*#-<!>"
	for name, st := range testStyles {
		for n := 0; n < 3000; n++ {
			b := make([]byte, r.Intn(40))
			for i := range b {
				b[i] = alphabet[r.Intn(len(alphabet))]
			}
			text := string(b)
			trimmed := strings.TrimSpace(text)
			lt := newLineText(text, trimmed, st, len(text))
			for at := 0; at <= len(text); at++ {
				if got, want := lt.inComment(at), refInComment(text[:at], trimmed, st); got != want {
					t.Fatalf("%s %q at %d: comment %v, want %v", name, text, at, got, want)
				}
				if got, want := lt.matches.inString(at), refInString(text[:at], st); got != want {
					t.Fatalf("%s %q at %d: string %v, want %v", name, text, at, got, want)
				}
			}
			// classify with a few matches, as the search finds them
			var spans [][2]int
			for at := 0; at < len(text); at += 1 + r.Intn(8) {
				spans = append(spans, [2]int{at, min(len(text), at+1)})
			}
			h := search.Hit{Line: 1, Text: text, Spans: spans}
			if got, want := classify(h, st), refClassify(h, st); got != want {
				t.Fatalf("%s %q %v: classify %q, want %q", name, text, spans, got, want)
			}
		}
	}
}

// A match after fieldLimit can't be a field's name: the regexps isField
// asks don't match any text that reaches past it.
func TestFieldLimit(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	const alphabet = "pub(crate) public val x: Map<K, V>[]?*&,=;\t\"#"
	for n := 0; n < 20000; n++ {
		b := make([]byte, r.Intn(30))
		for i := range b {
			b[i] = alphabet[r.Intn(len(alphabet))]
		}
		text := string(b)
		for _, typed := range []bool{true, false} {
			re := fieldBefore
			if typed {
				re = typedBefore
			}
			limit := fieldLimit(text, typed)
			for a := limit + 1; a <= len(text); a++ {
				if re.MatchString(text[:a]) {
					t.Fatalf("typed %v %q: matches up to %d, past the limit %d", typed, text, a, limit)
				}
			}
		}
	}
	for _, c := range []struct {
		text  string
		typed bool
	}{
		{"pub(in crate::x) name: T", false}, {"  private final Map<String, Integer> name;", true}, {"\tpublic readonly name = 1", false},
	} {
		if a := strings.Index(c.text, "name"); fieldLimit(c.text, c.typed) < a {
			t.Errorf("%q: limit %d before the field's name", c.text, fieldLimit(c.text, c.typed))
		}
	}
}

// classify reads a long line with many matches and comment markers once:
// 16 times the line must not take anywhere near 256 times as long.
func TestClassifyLongLines(t *testing.T) {
	for _, c := range []struct{ name, unit string }{
		{"a.js", `x("https://example.com/render", render); `}, // markers in strings, matches in code
		{"a.js", `"render https://example.com" + `},           // matches and markers in strings
		{"a.js", "`${render}` + /a\\/render/ + '//' + "},      // templates and regex literals
		{"a.py", `f"{render}" # render ' `},                   // in a comment from the first #
		{"a.go", `s := "/* render" + x /* render */ + `},      // block comments and strings
		{"a.rs", `'"' render "x" `},                           // character literals
	} {
		st := styleOf(c.name)
		hit := func(n int) search.Hit {
			text := strings.Repeat(c.unit, n)
			var spans [][2]int
			for i := 0; ; {
				j := strings.Index(text[i:], "render")
				if j < 0 {
					break
				}
				spans = append(spans, [2]int{i + j, i + j + 6})
				i += j + 6
			}
			return search.Hit{Line: 1, Text: text, Spans: spans}
		}
		small, big := hit(200), hit(3200)
		ts, tb := fastest(small, st), fastest(big, st)
		if tb > 64*ts+5*time.Millisecond {
			t.Errorf("%s %q: 200 repeats took %v, 3200 took %v", c.name, c.unit, ts, tb)
		}
		if got, want := classify(small, st), refClassify(small, st); got != want {
			t.Errorf("%s %q: classify %q, want %q", c.name, c.unit, got, want)
		}
	}
}

// fastest is the shortest of a few runs of classify.
func fastest(h search.Hit, st style) time.Duration {
	best := time.Duration(1 << 62)
	for range 3 {
		start := time.Now()
		classify(h, st)
		best = min(best, time.Since(start))
	}
	return best
}
