package group

import (
	"sort"
	"strings"
)

// lineText tells, for the matches on one line, whether each lies in a
// comment or a string literal, judging the text before it. It reads the
// line once, however many matches and comment markers it has. classify
// used to read the line from its start for every match, and again for
// every comment marker before it: a 3 MB line with hundreds of matches
// took minutes. refInComment and refInString in the tests are that
// reading, which lineText must answer exactly like: the ${…} of a
// template and the {…} of a Python f-string are code, a character literal
// ('"') opens nothing, and a JavaScript regex literal counts as a string.
type lineText struct {
	text, trimmed string
	st            style
	last          int // the start of the last match: nothing after it matters

	matches strScan   // inString before each match
	marks   []strScan // inString before each line comment marker
	firsts  []int     // the first line comment marker outside a string, per marker; -2: not looked for
	blocks  strScan   // inString before the /* that may open a comment

	opens, closes []int // where /* and */ start, when looked for
	looked        bool
	html, htmlEnd int // the first <!-- and -->, or -1
}

func newLineText(text, trimmed string, st style, last int) *lineText {
	lt := &lineText{text: text, trimmed: trimmed, st: st, last: last, html: -1, htmlEnd: -1}
	lt.matches = strScan{text: text, st: st}
	lt.blocks = strScan{text: text, st: st}
	lt.marks = make([]strScan, len(st.line))
	lt.firsts = make([]int, len(st.line))
	for k := range st.line {
		lt.marks[k] = strScan{text: text, st: st}
		lt.firsts[k] = -2
	}
	if st.line == nil && !st.block {
		lt.html, lt.htmlEnd = strings.Index(text, "<!--"), strings.Index(text, "-->")
	}
	return lt
}

// inComment: the text before at ends in a comment (refInComment).
func (lt *lineText) inComment(at int) bool {
	st := lt.st
	for k, m := range st.line {
		if strings.HasPrefix(lt.trimmed, m) {
			return true
		}
		// a marker earlier on the line, outside a string
		if i := lt.firstMark(k, m); i >= 0 && i+len(m) <= at {
			return true
		}
	}
	if st.block {
		if strings.HasPrefix(lt.trimmed, "/*") || strings.HasPrefix(lt.trimmed, "* ") || lt.trimmed == "*" || strings.HasPrefix(lt.trimmed, "*/") {
			return true
		}
		if !lt.looked {
			lt.opens, lt.closes, lt.looked = positions(lt.text, "/*"), positions(lt.text, "*/"), true
		}
		// the last /* before the match, with no */ after it, outside a string
		k := sort.Search(len(lt.opens), func(k int) bool { return lt.opens[k]+2 > at }) - 1
		if k >= 0 {
			i := lt.opens[k]
			c := sort.SearchInts(lt.closes, i)
			if (c == len(lt.closes) || lt.closes[c]+2 > at) && !lt.blocks.inString(i) {
				return true
			}
		}
	}
	if st.line == nil && !st.block && lt.html >= 0 && lt.html+4 <= at && !(lt.htmlEnd >= 0 && lt.htmlEnd+3 <= at) {
		return true
	}
	return false
}

// firstMark is where the first marker m (st.line[k]) outside a string
// starts, counting the markers as refInComment does (each search starts
// after the last one found), or -1 if there is none before the last match.
func (lt *lineText) firstMark(k int, m string) int {
	if lt.firsts[k] != -2 {
		return lt.firsts[k]
	}
	lt.firsts[k] = -1
	text := lt.text
	for i := strings.Index(text, m); i >= 0 && i+len(m) <= lt.last; {
		if !lt.marks[k].inString(i) && (m != "#" || i == 0 || text[i-1] == ' ' || text[i-1] == '\t') {
			lt.firsts[k] = i
			break
		}
		j := strings.Index(text[i+len(m):], m)
		if j < 0 {
			break
		}
		i += len(m) + j
	}
	return lt.firsts[k]
}

// positions lists where s starts in text.
func positions(text, s string) []int {
	var at []int
	for i := 0; ; {
		j := strings.Index(text[i:], s)
		if j < 0 {
			return at
		}
		at = append(at, i+j)
		i += j + 1
	}
}

// strScan answers refInString(text[:at], st) for growing at, reading text
// once: it keeps the state of that loop at the last step that didn't
// depend on where the text ends, and goes on from there.
type strScan struct {
	text string
	st   style
	done strState // the state after the steps that hold for any end at or after the last asked
	last int      // the last at asked
	// a regex literal found at re: where it ends on the whole line (-1:
	// it doesn't), so asking again inside it costs nothing
	re, reEnd int
	reKnown   bool
}

// strState is the state of the string loop before step i.
type strState struct {
	i      int
	open   byte
	interp bool // the open string interpolates: ` or f"
	depth  int  // inside ${…} or {…} of an interpolating string
}

func (s *strScan) inString(at int) bool {
	if at < s.last {
		*s = strScan{text: s.text, st: s.st} // asked out of order: start again
	}
	s.last = at
	cur := s.done
	commit := true // the steps so far hold for any end at or after at
	for cur.i < at {
		next, looks, inRegex := s.step(cur, at)
		if inRegex {
			return true // the match is inside the regex literal
		}
		cur = next
		if commit && looks < at {
			s.done = cur
		} else {
			commit = false
		}
	}
	return cur.open != 0 && cur.depth == 0
}

// step is one turn of the string loop over text[:end], from state c. It
// returns the next state, the furthest byte the turn depended on, and
// whether the text ends inside a regex literal (an early true).
func (s *strScan) step(c strState, end int) (next strState, looks int, inRegex bool) {
	before, st := s.text[:end], s.st
	i := c.i
	ch := before[i]
	looks = i
	if c.depth > 0 {
		switch ch {
		case '{':
			c.depth++
		case '}':
			c.depth--
		}
		c.i = i + 1
		return c, looks, false
	}
	if ch == '\\' {
		c.i = i + 2
		return c, looks, false
	}
	if c.open != 0 {
		switch {
		case ch == c.open:
			c.open = 0
		case c.interp && c.open == '`' && ch == '$':
			looks = i + 1
			if i+1 < len(before) && before[i+1] == '{' {
				c.depth, i = 1, i+1
			}
		case c.interp && c.open != '`' && ch == '{':
			looks = i + 1
			if i+1 < len(before) && before[i+1] == '{' {
				i++ // {{ is a literal brace
			} else {
				c.depth = 1
			}
		}
		c.i = i + 1
		return c, looks, false
	}
	if ch == '/' && st.js {
		looks = i + 1
		if i+1 < len(before) && before[i+1] != '/' && before[i+1] != '*' && regexStart(before[:i]) {
			if !s.reKnown || s.re != i {
				s.re, s.reEnd, s.reKnown = i, regexEnd(s.text, i), true
			}
			// regexEnd(before, i) finds the same end when it lies before
			// the end of before, else none
			if s.reEnd < 0 || s.reEnd-1 >= len(before) {
				return c, len(s.text), true
			}
			c.i = s.reEnd
			return c, s.reEnd - 1, false
		}
	}
	if ch == '\'' && strings.IndexByte(st.quotes, '\'') < 0 {
		// a character literal: 'x', '\n', '"' (Rust lifetimes have no closing quote)
		looks = i + 3
		if i+2 < len(before) && before[i+2] == '\'' {
			i += 2
		} else if i+3 < len(before) && before[i+1] == '\\' && before[i+3] == '\'' {
			i += 3
		}
		c.i = i + 1
		return c, looks, false
	}
	if strings.IndexByte(st.quotes, ch) >= 0 {
		if !(ch == '\'' && i > 0 && isWord(before[i-1]) && !fPrefix(before[:i])) { // not it's, don't
			c.open = ch
			c.interp = ch == '`' || st.fstrings && fPrefix(before[:i])
		}
	}
	c.i = i + 1
	return c, looks, false
}
