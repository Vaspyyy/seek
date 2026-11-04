// Package render prints search results within a line budget: every file
// gets a line, then each file a line per definition with hits, then the
// hit lines themselves (or whole definitions with -s), in order of rank.
package render

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/Vaspyyy/seek/internal/group"
	"github.com/Vaspyyy/seek/internal/search"
)

// Options control the output.
type Options struct {
	Budget  int  // roughly how many lines to print
	Color   bool // ANSI colors
	Context int  // lines of context around each hit (-C)
	Bodies  bool // whole definitions (-s)
	Counts  bool // at most one line per definition (-c)
}

type palette struct{ bold, faint, path, sym, match, label, reset string }

func colors(on bool) palette {
	if !on {
		return palette{}
	}
	return palette{bold: "\x1b[1m", faint: "\x1b[2m", path: "\x1b[1;34m", sym: "\x1b[36m", match: "\x1b[1;31m", label: "\x1b[33m", reset: "\x1b[0m"}
}

// Rank orders files: source, then tests, docs, data, and last lock files,
// generated, minified and vendored code.
func Rank(f *group.File) int {
	p := strings.ToLower(f.Path)
	base := path.Base(p)
	switch {
	case f.Kind != "":
		return 5
	case strings.Contains(p, "testdata/") || strings.Contains(p, "fixtures/") || strings.Contains(p, "snapshots/") || strings.Contains(p, "__snapshots__"):
		return 4
	case strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".txt") || strings.HasSuffix(p, ".rst") || strings.HasPrefix(base, "changelog"):
		return 3
	case strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") || strings.Contains(base, ".test.") ||
		strings.Contains(base, "smoke") || strings.Contains(base, "bench") || strings.Contains("/"+p, "/e2e/") ||
		strings.Contains(base, ".spec.") || strings.Contains("/"+p, "/tests/") || strings.Contains("/"+p, "/test/") || strings.HasSuffix(base, "_test.py"):
		return 2
	case !f.Outlined && dataExt[strings.TrimPrefix(path.Ext(base), ".")]:
		return 3
	}
	return 0
}

var dataExt = map[string]bool{"json": true, "yaml": true, "yml": true, "toml": true, "csv": true, "tsv": true, "xml": true, "svg": true, "ini": true, "lock": true}

// Order sorts files: those with a definition first, then by rank, then by path.
func Order(files []*group.File) {
	sort.SliceStable(files, func(i, j int) bool {
		a, b := files[i], files[j]
		da, db := len(a.Defs) > 0 && a.Kind == "", len(b.Defs) > 0 && b.Kind == ""
		if da != db {
			return da
		}
		if ra, rb := Rank(a), Rank(b); ra != rb {
			return ra < rb
		}
		return a.Path < b.Path
	})
}

type writer struct {
	w io.Writer
	c palette
	o Options
}

func (w *writer) line(format string, args ...any) {
	fmt.Fprintf(w.w, format+"\n", args...)
}

// Layout is how much of each file fits the budget.
type Layout struct {
	level []int // per shown file: 1 one line, 2 a line per definition, 3 hit lines, 4 whole definitions
	ctx   []int // per shown file with -C: the context lines that fit (at most -C)
}

// Reduced counts the shown files that didn't get what -s or -C asked for
// (whole definitions, context lines), of all shown files that could have.
func (l *Layout) Reduced(files []*group.File, o Options) (reduced, total int) {
	want := 0
	switch {
	case o.Bodies:
		want = 5
	case o.Context > 0 && !o.Counts:
		want = 3
	default:
		return 0, 0
	}
	for i, lv := range l.level {
		if files[i].Kind != "" {
			continue
		}
		total++
		if lv < want || l.ctx != nil && l.ctx[i] < o.Context {
			reduced++
		}
	}
	return reduced, total
}

// Visible reports whether file i's definitions can be seen in the output.
func (l *Layout) Visible(i int, f *group.File) bool {
	return i < len(l.level) && (l.level[i] >= 3 || l.level[i] == 2 && f.Outlined)
}

// Files prints the files (already ordered) within the budget.
func Files(out io.Writer, files []*group.File, o Options) {
	Print(out, files, Plan(files, o), o)
}

// Plan decides how much of each file is shown: every file gets a line,
// then, in order, a line per definition, then the hit lines (or bodies).
func Plan(files []*group.File, o Options) *Layout {
	w := &writer{o: o}
	room := max(o.Budget, 1)
	shown := len(files)
	if shown > room {
		shown = max(room-1, 1)
	}
	level := make([]int, shown)
	for i := range level {
		level[i] = 1
	}
	room -= shown
	ctx := make([]int, shown)
	for i := range ctx {
		ctx[i] = o.Context
	}
	cost := func(i, lv int) int {
		cw := *w
		cw.o.Context = ctx[i]
		return cw.cost(files[i], lv)
	}
	// fits raises file i to level lv if that fits the room left; with -C,
	// with as much context as fits (down to none) rather than not at all
	fits := func(i, lv int) bool {
		was := ctx[i]
		for c := o.Context; ; c /= 2 {
			old := cost(i, level[i])
			ctx[i] = c
			if d := cost(i, lv) - old; d <= room {
				room -= d
				level[i] = lv
				return true
			}
			ctx[i] = was
			if c == 0 || lv < 3 {
				return false
			}
		}
	}
	top := 3
	if o.Counts {
		top = 2
	} else if o.Bodies {
		top = 5
	}
	for lv := 2; lv <= min(top, 3); lv++ {
		for i := range level {
			if files[i].Kind != "" || level[i] != lv-1 {
				continue // lock, generated and vendored files stay on one line
			}
			if lv == 3 && top == 5 {
				// -s: whole definitions for the first files that fit, in order,
				// else the lines around their hits, rather than hit lines for all
				if fits(i, 5) || fits(i, 4) {
					continue
				}
			}
			fits(i, lv)
		}
	}
	if top == 5 { // what's left: more code for files that got less
		for _, want := range []int{4, 5} {
			for i := range level {
				if level[i] >= 3 && level[i] < want {
					fits(i, want)
				}
			}
		}
	}
	l := &Layout{level: level}
	if o.Context > 0 {
		l.ctx = ctx
	}
	return l
}

// Print prints the files as planned.
func Print(out io.Writer, files []*group.File, l *Layout, o Options) {
	w := &writer{w: out, c: colors(o.Color), o: o}
	for i, lv := range l.level {
		if l.ctx != nil {
			w.o.Context = l.ctx[i]
		}
		w.file(files[i], max(lv, 1))
	}
	shown := len(l.level)
	if n := len(files) - shown; n > 0 {
		hits := 0
		var names []string
		for _, f := range files[shown:] {
			hits += f.Count()
			if len(names) < 5 {
				names = append(names, f.Path)
			}
		}
		more := ""
		if n > len(names) {
			more = fmt.Sprintf(", +%d more", n-len(names))
		}
		w.line("%s… %s with %s: %s%s (raise -b, or narrow with -g or a path)%s", w.c.faint, plural(n, "more file"), plural(hits, "hit"), strings.Join(names, ", "), more, w.c.reset)
	}
}

// cost is how many lines a file takes at a level (1: one line, 2: a line
// per definition, 3: hit lines, 4: lines around each hit, 5: whole
// definitions, or the lines around the hits in long ones).
func (w *writer) cost(f *group.File, lv int) int {
	switch lv {
	case 0, 1:
		return 1
	case 2:
		if !f.Outlined {
			return 1
		}
		return 1 + len(f.Groups)
	case 3:
		n := 1
		for _, g := range f.Groups {
			if f.Outlined {
				n++
			}
			n += len(w.window(f, g, 0))
		}
		return n
	}
	n := 1
	for _, g := range f.Groups {
		if f.Outlined {
			n++
		}
		n += len(w.window(f, g, lv-3))
	}
	return n
}

func (w *writer) file(f *group.File, lv int) {
	c := w.c
	n := f.Count()
	desc := []string{plural(f.Lines, "line") + crlf(f)}
	if f.Kind != "" {
		desc = append(desc, f.Kind)
	}
	hits := plural(n, "hit")
	if labels := labelCounts(f); labels != "" {
		hits += " (" + labels + ")"
	}
	desc = append(desc, hits)
	head := fmt.Sprintf("%s%s%s  %s", c.path, f.Path, c.reset, strings.Join(desc, ", "))
	switch {
	case lv == 1 && f.Kind != "":
		var ls []string
		var snips []string
		for _, g := range f.Groups {
			for _, h := range g.Hits {
				ls = append(ls, strconv.Itoa(h.Line))
				snips = append(snips, "L"+strconv.Itoa(h.Line)+" "+w.snippet(h))
			}
		}
		if len(snips) <= 2 {
			w.line("%s: %s", head, strings.Join(snips, "  "))
			return
		}
		w.line("%s: L%s", head, joinCapped(ls, 8))
		return
	case lv == 1 && f.Outlined && len(f.Groups) > 0:
		w.line("%s in %s: %s", head, plural(len(f.Groups), "place"), w.topGroups(f, 4))
		return
	case lv == 1 || lv == 2 && !f.Outlined:
		var ls []string
		for _, g := range f.Groups {
			for _, h := range g.Hits {
				ls = append(ls, strconv.Itoa(h.Line))
			}
		}
		w.line("%s: L%s", head, joinCapped(ls, 12))
		return
	}
	// the hit lines follow: they show the labels themselves
	w.line("%s%s%s  %s%s", c.path, f.Path, c.reset, plural(f.Lines, "line"), crlf(f))
	width := len(strconv.Itoa(lastLine(f)))
	for _, g := range f.Groups {
		ind := "  "
		if lv == 2 {
			w.line("  %s  %s", w.groupHead(g), w.groupCount(g))
			continue
		}
		if f.Outlined {
			w.line("  %s", w.groupHead(g))
			ind = "    "
		}
		common := commonIndent(g)
		shown := w.window(f, g, lv-3)
		ctx := w.o.Context
		for _, l := range shown {
			if l.hit == nil && !l.gap {
				ctx = max(ctx, 1) // context lines (block openers): mark hits with ":"
				if lv == 3 && w.o.Context == 0 {
					common = sharedIndent(common, l.text) // openers sit left of their hits
				}
			}
		}
		for _, l := range shown {
			if l.gap {
				w.line("%s%s%s…%s", ind, c.faint, strings.Repeat(" ", width-1), c.reset)
				continue
			}
			text := l.text
			if l.hit != nil {
				text = w.highlight(*l.hit, lv >= 4 || w.o.Context > 0, common)
				if shownLabel[l.hit.Label] {
					text += "  " + c.label + "(" + l.hit.Label + ")" + c.reset
				}
				w.line("%s%s%*d%s%s %s", ind, c.faint, width, l.n, sep(':', lv, ctx), c.reset, text)
			} else {
				if lv == 3 && w.o.Context == 0 { // a block opener: shown like the hits
					text = w.highlight(group.Hit{Hit: search.Hit{Text: text}}, false, common)
				}
				w.line("%s", strings.TrimRight(fmt.Sprintf("%s%s%*d%s%s %s", ind, c.faint, width, l.n, sep('-', lv, ctx), c.reset, clipLine(text, 300)), " "))
			}
		}
	}
}

// shownLabel: labels a hit line gets; the others (definition, import,
// declaration, assignment) are plain from the line itself.
var shownLabel = map[string]bool{"comment": true, "string": true}

// commonIndent is the leading whitespace all of a group's hit lines share.
func commonIndent(g *group.Group) string {
	var common string
	for k, h := range g.Hits {
		if k == 0 {
			common = h.Text[:len(h.Text)-len(strings.TrimLeft(h.Text, " \t"))]
			continue
		}
		common = sharedIndent(common, h.Text)
	}
	return common
}

// sharedIndent is the part of common that line's indentation starts with.
func sharedIndent(common, line string) string {
	ind := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	n := 0
	for n < len(common) && n < len(ind) && common[n] == ind[n] {
		n++
	}
	return common[:n]
}

// sep is grep's marker after a line number (":" for hits, "-" for
// context), shown only when there are context lines to tell apart.
func sep(ch byte, lv, ctx int) string {
	if lv >= 4 || ctx > 0 {
		return string(ch)
	}
	return ""
}

// crlf notes Windows line endings, which the shown text leaves out.
func crlf(f *group.File) string {
	if bytes.Contains(f.Data, []byte("\r\n")) {
		return ", CRLF line endings"
	}
	return ""
}

func lastLine(f *group.File) int {
	n := 1
	for _, g := range f.Groups {
		if w := g.End; w > n {
			n = w
		}
		for _, h := range g.Hits {
			n = max(n, h.Line)
		}
	}
	return min(n, f.Lines)
}

func (w *writer) groupHead(g *group.Group) string {
	c := w.c
	if g.Sym < 0 {
		return c.faint + "(top level)" + c.reset
	}
	rng := "L" + strconv.Itoa(g.First())
	if g.End > g.First() {
		rng += "-" + strconv.Itoa(g.End)
	}
	return fmt.Sprintf("%s %s%s%s  %s", g.Kind, c.sym, g.Name, c.reset, rng)
}

// groupCount is a definition's hits on one line: "14 hits (1 assignment): L11678, L11687, …".
func (w *writer) groupCount(g *group.Group) string {
	var ls []string
	labels := map[string]int{}
	for _, h := range g.Hits {
		ls = append(ls, strconv.Itoa(h.Line))
		if h.Label != "" {
			labels[h.Label]++
		}
	}
	s := plural(len(g.Hits), "hit")
	var parts []string
	for _, l := range []string{"definition", "declaration", "assignment", "import", "comment", "string"} {
		if n := labels[l]; n > 0 {
			parts = append(parts, labelCount(l, n))
		}
	}
	if len(parts) > 0 {
		if n := len(g.Hits) - sumLabels(labels); n > 0 {
			parts = append([]string{plural(n, "use")}, parts...)
		}
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	return s + ": L" + joinCapped(ls, 8)
}

func sumLabels(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func labelCount(l string, n int) string {
	if n == 1 {
		if l == "comment" || l == "string" {
			return "1 in a " + l
		}
		return "1 " + l
	}
	return fmt.Sprintf("%d %s", n, plurals[l])
}

// topGroups names the definitions with the most hits: "tick 9, render 7".
func (w *writer) topGroups(f *group.File, n int) string {
	gs := append([]*group.Group(nil), f.Groups...)
	sort.SliceStable(gs, func(i, j int) bool { return len(gs[i].Hits) > len(gs[j].Hits) })
	var parts []string
	for _, g := range gs[:min(n, len(gs))] {
		name := g.Name
		if g.Sym < 0 {
			name = "(top level)"
		}
		parts = append(parts, fmt.Sprintf("%s %d", name, len(g.Hits)))
	}
	if len(gs) > n {
		parts = append(parts, fmt.Sprintf("+%d more", len(gs)-n))
	}
	return strings.Join(parts, ", ")
}

func labelCounts(f *group.File) string {
	var parts []string
	plainUses := f.Labelled("")
	for _, l := range []string{"definition", "declaration", "assignment", "import", "comment", "string"} {
		if n := f.Labelled(l); n > 0 {
			parts = append(parts, labelCount(l, n))
		}
	}
	if len(parts) > 0 && plainUses > 0 { // the counts add up to the total
		parts = append([]string{plural(plainUses, "use")}, parts...)
	}
	return strings.Join(parts, ", ")
}

var plurals = map[string]string{"definition": "definitions", "declaration": "declarations", "assignment": "assignments",
	"import": "imports", "comment": "in comments", "string": "in strings"}

type shownLine struct {
	n    int
	text string
	hit  *group.Hit
	gap  bool
}

// bigDef is the length above which a definition's hits also show the line
// that opens the block they're in (the if, loop or callback around them).
const bigDef = 150

// withOpeners lists a big definition's hits, each after the nearest line
// above it that is indented less (its block's first line), once.
func withOpeners(f *group.File, g *group.Group, lines []string) []shownLine {
	hits := map[int]bool{}
	for _, h := range g.Hits {
		hits[h.Line] = true
	}
	added := map[int]bool{}
	var out []shownLine
	for i := range g.Hits {
		h := &g.Hits[i]
		if o := opener(lines, h.Line, g.Line); o > 0 && !hits[o] && !added[o] {
			added[o] = true
			out = append(out, shownLine{n: o, text: lines[o-1]})
		}
		out = append(out, shownLine{n: h.Line, hit: h})
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].n < out[b].n })
	return out
}

// opener is the nearest line above n, after the definition's first line,
// indented less than n (0 if none within 300 lines).
func opener(lines []string, n, def int) int {
	if n < 1 || n > len(lines) {
		return 0
	}
	ind := indentWidth(lines[n-1])
	for k := n - 1; k > def && n-k <= 300; k-- {
		t := strings.TrimSpace(lines[k-1])
		if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "*") || t == "}" || t == ")" || t == "]" {
			continue
		}
		if indentWidth(lines[k-1]) < ind {
			return k
		}
	}
	return 0
}

func indentWidth(l string) int {
	n := 0
	for _, c := range l {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// bodyMax is the longest definition printed whole with -s; longer ones
// show their first line and the lines around each hit.
const bodyMax = 80

// window is the lines a group shows: its hits (with -C context), or with
// whole set its whole definition (or windows around the hits).
// mode 0: the hits (with -C context); 1: lines around each hit and the
// definition's first line; 2: the whole definition if it's short, else as 1.
func (w *writer) window(f *group.File, g *group.Group, mode int) []shownLine {
	ctx := w.o.Context
	hits := map[int]*group.Hit{}
	for i := range g.Hits {
		hits[g.Hits[i].Line] = &g.Hits[i]
	}
	if mode == 0 && ctx == 0 {
		var out []shownLine
		if g.Sym >= 0 && g.End-g.Line > bigDef {
			return withOpeners(f, g, splitLines(f.Data))
		}
		for i := range g.Hits {
			out = append(out, shownLine{n: g.Hits[i].Line, hit: &g.Hits[i]})
		}
		return out
	}
	lines := splitLines(f.Data)
	want := map[int]bool{}
	first := g.First() // a hit in the doc comment above: start there
	if mode == 2 {
		for _, o := range f.Groups {
			if o != g && o.Sym >= 0 && g.Sym >= 0 && o.First() > g.Line && o.End <= g.End {
				mode = 1 // it contains other shown definitions: don't print them twice
				break
			}
		}
	}
	if mode == 2 && g.Sym >= 0 && g.End-first+1 <= bodyMax {
		for n := first; n <= g.End; n++ {
			want[n] = true
		}
	} else {
		if mode >= 1 {
			ctx = max(ctx, 3)
			if g.Sym >= 0 {
				want[g.Line] = true
			}
		}
		for _, h := range g.Hits {
			for n := h.Line - ctx; n <= h.Line+ctx; n++ {
				if n >= 1 && n <= len(lines) && (g.Sym < 0 || n >= first && n <= g.End || mode == 0) {
					want[n] = true
				}
			}
		}
	}
	var ns []int
	for n := range want {
		if n >= 1 && n <= len(lines) {
			ns = append(ns, n)
		}
	}
	sort.Ints(ns)
	var out []shownLine
	for k, n := range ns {
		if k > 0 && n > ns[k-1]+1 {
			out = append(out, shownLine{gap: true})
		}
		out = append(out, shownLine{n: n, text: lines[n-1], hit: hits[n]})
	}
	if mode >= 1 && g.Sym >= 0 && len(ns) > 0 && ns[len(ns)-1] < g.End {
		out = append(out, shownLine{gap: true}) // the definition goes on
	}
	return out
}

func splitLines(data []byte) []string {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// highlight prints a hit line with its matches marked, clipped around the
// first match if it's long. raw keeps its indentation.
func (w *writer) highlight(h group.Hit, raw bool, common string) string {
	text := h.Text
	spans := h.Spans
	if !raw {
		// the indentation the group's lines share goes; the rest shows nesting
		rest := strings.TrimPrefix(text, common)
		body := strings.TrimLeft(rest, " \t")
		lead := strings.ReplaceAll(rest[:len(rest)-len(body)], "\t", "  ")
		cut := len(text) - len(body)
		text = lead + strings.TrimRight(body, " \t")
		spans = shift(spans, len(lead)-cut, len(text))
	}
	const maxLen = 160
	if len(text) > maxLen && len(spans) > 0 {
		from := max(0, spans[0][0]-50)
		for from > 0 && !utf8Start(text[from]) {
			from--
		}
		to := min(len(text), from+maxLen)
		for to < len(text) && !utf8Start(text[to]) {
			to++
		}
		pre, post := "", ""
		if from > 0 {
			pre = "…"
		}
		if to < len(text) {
			post = "…"
		}
		spans = shift(spans, -from, to-from)
		text = text[from:to]
		return pre + w.mark(text, spans) + post
	}
	return w.mark(text, spans)
}

// snippet is a short piece of a hit line around its first match.
func (w *writer) snippet(h group.Hit) string {
	text, spans := strings.TrimSpace(h.Text), h.Spans
	if cut := len(h.Text) - len(strings.TrimLeft(h.Text, " \t")); cut > 0 {
		spans = shift(spans, -cut, len(text))
	}
	if len(text) <= 80 || len(spans) == 0 {
		return w.mark(clipLine(text, 80), shift(spans, 0, min(len(text), 80)))
	}
	from := max(0, spans[0][0]-30)
	for from > 0 && !utf8Start(text[from]) {
		from--
	}
	to := min(len(text), from+80)
	for to < len(text) && !utf8Start(text[to]) {
		to++
	}
	pre, post := "", ""
	if from > 0 {
		pre = "…"
	}
	if to < len(text) {
		post = "…"
	}
	return pre + w.mark(text[from:to], shift(spans, -from, to-from)) + post
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func shift(spans [][2]int, by, limit int) [][2]int {
	var out [][2]int
	for _, s := range spans {
		a, b := s[0]+by, s[1]+by
		if b <= 0 || a >= limit {
			continue
		}
		out = append(out, [2]int{max(a, 0), min(b, limit)})
	}
	return out
}

func (w *writer) mark(text string, spans [][2]int) string {
	if w.c.match == "" || len(spans) == 0 {
		return text
	}
	var b strings.Builder
	at := 0
	for _, s := range spans {
		if s[0] < at {
			continue
		}
		b.WriteString(text[at:s[0]])
		b.WriteString(w.c.match + text[s[0]:s[1]] + w.c.reset)
		at = s[1]
	}
	b.WriteString(text[at:])
	return b.String()
}

func clipLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func joinCapped(ls []string, n int) string {
	if len(ls) <= n {
		return strings.Join(ls, ", ")
	}
	return strings.Join(ls[:n], ", ") + fmt.Sprintf(", +%d more", len(ls)-n)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	switch {
	case strings.HasSuffix(word, "s"):
		return fmt.Sprintf("%d %ses", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
