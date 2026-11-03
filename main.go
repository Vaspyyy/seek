// seek is a code search for people and AI coding agents: hits grouped by
// the function, class or section that contains them, labelled as
// definition, assignment, import, comment and so on, within a line budget.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Vaspyyy/seek/internal/files"
	"github.com/Vaspyyy/seek/internal/group"
	"github.com/Vaspyyy/seek/internal/render"
	"github.com/Vaspyyy/seek/internal/search"
	"github.com/Vaspyyy/seek/internal/suggest"
	"golang.org/x/term"
)

const version = "0.1.0"

const usage = `seek: search code, with hits grouped by the definition that contains them

usage: seek [flags] PATTERN [PATH...]

A pattern is a regular expression (Go syntax). One that is an identifier,
or several joined by | ("setSpeed", "Repo.Open", "a|b|c"), matches whole
words only. An invalid regex is searched as plain text.

  -d          definitions only: where PATTERN is defined, one line each
  -s          show the whole definition around each hit (windows in long ones)
  -c          counts: at most one line per definition
  -l          only the paths of files with hits (-0: separated by NUL bytes)
  -C N        N lines of context around each hit
  -i          ignore case
  -F          plain text, not a regex
  -w          whole words, also for a regex
  --partial   identifiers also inside longer words (speed finds speedMult)
  -g GLOB     only files matching GLOB ("*.js", "src/**", "!*_test.go"); repeatable
  -u          also ignored files, caches and dot-folders
  -b N        about how many lines to print (default 100, with -s 300)
  -e PATTERN  a pattern that starts with "-"
  -A          plain output without colors (the default when not on a terminal)
  --version

Files are what git sees in a repository (tracked and untracked, not
ignored); elsewhere caches, build output and dot-folders are skipped.
Exit status: 0 if something matched, 1 if not, 2 on errors.
`

type options struct {
	pattern                    string
	havePattern                bool
	paths, globs               []string
	defs, bodies, counts, list bool
	context, budget            int
	budgetSet                  bool
	so                         search.Options
	plain, unrestrict, null    bool
}

func main() {
	out := bufio.NewWriterSize(os.Stdout, 64<<10)
	code := run(os.Args[1:], out, os.Stderr, term.IsTerminal(int(os.Stdout.Fd())))
	out.Flush()
	os.Exit(code)
}

func run(args []string, out *bufio.Writer, errw io.Writer, tty bool) int {
	o, code, msg := parse(args)
	if msg != "" {
		if code == 0 {
			fmt.Fprint(out, msg)
			return 0
		}
		fmt.Fprintln(errw, "seek: "+msg)
		return code
	}
	color := tty && !o.plain && os.Getenv("NO_COLOR") == ""
	expr := o.pattern
	if o.defs {
		expr = defsExpr(expr)
	}
	p, err := search.Compile(expr, o.so)
	if err != nil {
		fmt.Fprintln(errw, "seek: "+err.Error())
		return 2
	}
	p.DefMode = o.defs
	searched := p.Expr
	p.Expr = o.pattern // the header shows what was typed
	paths, err := files.List(o.paths, files.Options{Globs: o.globs, Unrestrict: o.unrestrict})
	if err != nil {
		fmt.Fprintln(errw, "seek: "+err.Error())
		return 2
	}
	found, st := search.Run(paths, p)
	gs := buildAll(found, p)
	if o.list {
		sort.Slice(gs, func(i, j int) bool { return gs[i].Path < gs[j].Path })
		end := "\n"
		if o.null {
			end = "\x00"
		}
		for _, f := range gs {
			fmt.Fprint(out, f.Path+end)
		}
		return exitCode(len(gs) > 0)
	}
	render.Order(gs)
	budget := o.budget
	if !o.budgetSet {
		budget = 100
		if o.bodies {
			budget = 300
		}
	}
	ro := render.Options{Budget: budget - 1, Color: color, Context: o.context, Bodies: o.bodies, Counts: o.counts}
	scope := scopeOf(o, st)
	if o.defs {
		return definitions(out, o, p, gs, scope, ro, paths, searched)
	}
	hits := 0
	for _, f := range gs {
		hits += f.Count()
	}
	if hits == 0 {
		fmt.Fprintf(out, "seek: %s in %s: no hits\n", describe(p), scope)
		skipped(out, o, true)
		hints(out, o, p, paths)
		return 1
	}
	head := fmt.Sprintf("seek: %s: %s in %s", describe(p), plural(hits, "hit"), plural(len(gs), "file"))
	if len(o.paths) > 0 || len(o.globs) > 0 || st.TooBig > 0 {
		head = fmt.Sprintf("seek: %s in %s: %s in %s", describe(p), scope, plural(hits, "hit"), plural(len(gs), "file"))
	}
	layout := render.Plan(gs, ro)
	if d := defsLine(gs, 3, p, layout); d != "" {
		head += "; defined at " + d
	}
	fmt.Fprintln(out, head)
	render.Print(out, gs, layout, ro)
	notes(out, o, p, paths, hits, layout, gs, ro)
	skipped(out, o, false)
	return 0
}

// skipped says which files named on the command line weren't searched
// (binary, too big), and, when nothing was found, the nested repositories
// that weren't searched.
func skipped(out *bufio.Writer, o options, none bool) {
	for _, path := range o.paths {
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			if _, why := search.Read(path); why != "" {
				fmt.Fprintf(out, "  not searched: %s is %s\n", path, why)
			}
		}
	}
	if n := len(files.Nested); n > 0 && none {
		names := files.Nested[:min(n, 4)]
		more := ""
		if n > len(names) {
			more = fmt.Sprintf(" +%d more", n-len(names))
		}
		fmt.Fprintf(out, "  not searched: %s inside: %s%s (give it as a path to search it)\n", plural(n, "repository"), strings.Join(names, ", "), more)
	}
}

// notes end the output with what it doesn't show: hits inside longer
// words, and what the budget left out of -s and -C.
func notes(out *bufio.Writer, o options, p *search.Pattern, paths []string, hits int, l *render.Layout, gs []*group.File, ro render.Options) {
	if n, words := longer(o, p, paths, hits); n > 0 {
		cmd := command("--partial ", o.pattern)
		if cmd != "" {
			cmd = ": " + cmd
		}
		fmt.Fprintf(out, "+%s inside longer words (%s)%s\n", plural(n, "more hit"), words, cmd)
	}
	if reduced, total := l.Reduced(gs, ro); reduced > 0 {
		what := "less than their whole definitions"
		if !ro.Bodies {
			what = "no context lines"
		}
		fmt.Fprintf(out, "(%d of %s with %s, to stay within the budget: raise -b, e.g. -b %d)\n", reduced, plural(total, "file"), what, max(ro.Budget*2, 300))
	}
}

// longer is what a whole-word search left out: the lines that match only
// inside longer words, and the most frequent of those words.
func longer(o options, p *search.Pattern, paths []string, wholeLines int) (int, string) {
	if !p.Smart || len(p.Words) > 16 {
		return 0, ""
	}
	so := o.so
	so.Partial = true
	q, err := search.Compile(o.pattern, so)
	if err != nil {
		return 0, ""
	}
	found, _ := search.Run(paths, q)
	lines := 0
	counts := map[string]int{}
	whole := map[string]bool{}
	for _, w := range p.Words {
		whole[strings.ToLower(lastPart(w))] = true
	}
	for _, f := range found {
		lines += len(f.Hits)
		for _, h := range f.Hits {
			seen := map[string]bool{}
			for _, s := range h.Spans {
				w := wordAt(h.Text, s[0], s[1])
				if !whole[strings.ToLower(w)] && !seen[w] {
					seen[w] = true
					counts[w]++
				}
			}
		}
	}
	extra := lines - wholeLines
	if extra <= 0 || len(counts) == 0 {
		return 0, ""
	}
	type wc struct {
		w string
		n int
	}
	var ws []wc
	for w, n := range counts {
		ws = append(ws, wc{w, n})
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].n > ws[j].n || ws[i].n == ws[j].n && ws[i].w < ws[j].w })
	var parts []string
	for _, x := range ws[:min(len(ws), 4)] {
		parts = append(parts, fmt.Sprintf("%s %d", x.w, x.n))
	}
	if len(ws) > 4 {
		parts = append(parts, fmt.Sprintf("+%d more", len(ws)-4))
	}
	return extra, strings.Join(parts, ", ")
}

// command is a seek command line to suggest, or "" if the pattern is too
// long to be worth repeating.
func command(flags string, pattern string) string {
	if len(pattern) > 120 {
		return ""
	}
	return "seek " + flags + shellQuote(pattern)
}

// buildAll groups the hits of every file, in parallel: outlining is the
// slow part when many files match.
func buildAll(found []*search.File, p *search.Pattern) []*group.File {
	gs := make([]*group.File, len(found))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				gs[i] = group.Build(found[i], p)
			}
		}()
	}
	for i := range found {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return gs
}

func exitCode(found bool) int {
	if found {
		return 0
	}
	return 1
}

// defsExpr: -d looks for the last part of a qualified name (Repo.Open is
// written "func (r *Repo) Open"), then keeps definitions named in full.
func defsExpr(expr string) string {
	alts := strings.Split(expr, "|")
	for i, a := range alts {
		if j := strings.LastIndexAny(a, ".:"); j >= 0 && j+1 < len(a) && !strings.ContainsAny(a, `\[](){}*+?^$`) {
			alts[i] = a[j+1:]
		}
	}
	return strings.Join(alts, "|")
}

// describe says how the pattern was read: "setSpeed" (whole word).
func describe(p *search.Pattern) string {
	expr := p.Expr
	if r := []rune(expr); len(r) > 120 {
		expr = string(r[:100]) + fmt.Sprintf("… (%d characters)", len(r))
	}
	s := `"` + expr + `"`
	var notes []string
	switch {
	case p.Bad != "" && strings.Contains(p.Bad, "Perl syntax"):
		notes = append(notes, "look-around and backreferences aren't supported (rg -P has them); searched as text")
	case p.Bad != "":
		notes = append(notes, "not a valid regex ("+p.Bad+"), searched as text")
	case p.Smart && len(p.Words) > 1:
		notes = append(notes, "whole words")
	case p.Smart:
		notes = append(notes, "whole word")
	case p.Text:
		notes = append(notes, "text")
	}
	if p.Fold {
		notes = append(notes, "ignoring case")
	}
	if len(notes) > 0 {
		s += " (" + strings.Join(notes, ", ") + ")"
	}
	return s
}

func scopeOf(o options, st search.Stats) string {
	if len(o.paths) == 1 && len(o.globs) == 0 && st.Files <= 1 {
		if fi, err := os.Stat(o.paths[0]); err == nil && !fi.IsDir() {
			return o.paths[0]
		}
	}
	n := plural(st.Files, "file")
	where := ""
	if len(o.paths) > 0 {
		where = strings.Join(o.paths, " ") + ", "
	}
	if len(o.globs) > 0 {
		n = fmt.Sprintf("%d %s files", st.Files, strings.Join(o.globs, " "))
		if st.Files == 1 {
			n = fmt.Sprintf("1 %s file", strings.Join(o.globs, " "))
		}
	}
	s := where + n
	if st.TooBig > 0 {
		s += fmt.Sprintf(" (%s over %d MB skipped)", plural(st.TooBig, "file"), search.MaxSize>>20)
	}
	return s
}

// defsLine lists where the pattern is defined: "src/a.js:12 (function f)".
// Only definitions the output doesn't show are listed.
func defsLine(gs []*group.File, n int, p *search.Pattern, l *render.Layout) string {
	var parts []string
	total := 0
	for i, f := range gs {
		if f.Kind != "" || l.Visible(i, f) {
			continue
		}
		for _, d := range f.Defs {
			total++
			if len(parts) < n {
				s := fmt.Sprintf("%s:%d", f.Path, d.Line)
				if len(p.Words) != 1 || lastPart(d.Name) != p.Words[0] {
					s += " (" + d.Name + ")" // say which, when it isn't plain from the pattern
				}
				parts = append(parts, s)
			}
		}
	}
	if total > n {
		parts = append(parts, fmt.Sprintf("+%d more (seek -d)", total-n))
	}
	return strings.Join(parts, ", ")
}

// definitions prints -d: one line per definition, or with -s each one whole.
func definitions(out *bufio.Writer, o options, p *search.Pattern, gs []*group.File, scope string, ro render.Options, paths []string, searched string) int {
	want := strings.Split(o.pattern, "|")
	keep := func(name string) bool {
		if !p.Smart {
			return true
		}
		for _, w := range want {
			if name == w || strings.HasSuffix(name, "."+w) || strings.HasSuffix(name, "::"+w) ||
				p.Fold && (strings.EqualFold(name, w) || strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(w))) {
				return true
			}
			if !strings.ContainsAny(w, ".:") && (lastPart(name) == w || p.Fold && strings.EqualFold(lastPart(name), w)) {
				return true
			}
		}
		return false
	}
	var defFiles []*group.File
	total, others := 0, 0
	for _, f := range gs {
		var kept []group.Def
		for _, d := range f.Defs {
			if keep(d.Name) {
				kept = append(kept, d)
			}
		}
		others += f.Count() - len(kept)
		if len(kept) > 0 {
			total += len(kept)
			defFiles = append(defFiles, onlyDefs(f, kept))
		}
	}
	// definitions the outline rules found come before declarations
	// and keyword matches; vendored and generated code last
	sort.SliceStable(defFiles, func(i, j int) bool {
		a, b := defFiles[i], defFiles[j]
		if (a.Kind == "") != (b.Kind == "") {
			return a.Kind == ""
		}
		return outlined(a) && !outlined(b)
	})
	have := map[string]bool{}
	for _, f := range defFiles {
		for _, d := range f.Defs {
			have[f.Path+":"+strconv.Itoa(d.Line)] = true
		}
	}
	if total == 0 {
		fmt.Fprintf(out, "seek -d: %s in %s: no definition found", describe(p), scope)
		if others > 0 {
			if cmd := command("", searched); cmd != "" {
				fmt.Fprintf(out, " (%s; %s lists them)", plural(others, "other hit"), cmd)
			} else {
				fmt.Fprintf(out, " (%s)", plural(others, "other hit"))
			}
		}
		fmt.Fprintln(out)
		if c := containing(o, p, paths, have); c != "" {
			fmt.Fprintln(out, "  definitions containing it: "+c)
		} else if others == 0 {
			hints(out, o, p, paths)
		}
		return 1
	}
	head := fmt.Sprintf("seek -d: %s in %s: %s", describe(p), scope, plural(total, "definition"))
	if others > 0 {
		head += fmt.Sprintf(", %s elsewhere", plural(others, "other hit"))
	}
	fmt.Fprintln(out, head)
	more := containing(o, p, paths, have)
	if o.bodies {
		render.Files(out, defFiles, ro)
		if more != "" {
			fmt.Fprintln(out, "definitions containing it: "+more)
		}
		return 0
	}
	shown := 0
	for _, f := range defFiles {
		for _, d := range f.Defs {
			if shown >= max(ro.Budget, 1) {
				fmt.Fprintf(out, "… %s (raise -b)\n", plural(total-shown, "more definition"))
				return 0
			}
			rng := strconv.Itoa(d.Line)
			if d.End > d.Line {
				rng += "-" + strconv.Itoa(d.End)
			}
			kind := ""
			if f.Kind != "" {
				kind = "  (" + f.Kind + ")"
			}
			fmt.Fprintf(out, "%s:%s  %s%s\n", f.Path, rng, d.Sig, kind)
			shown++
		}
	}
	if more != "" {
		fmt.Fprintln(out, "definitions containing it: "+more)
	}
	return 0
}

func outlined(f *group.File) bool {
	for _, d := range f.Defs {
		if d.Outline {
			return true
		}
	}
	return false
}

// containing lists definitions whose names contain one of the searched
// words (load_settings for load), which a whole-word -d leaves out.
func containing(o options, p *search.Pattern, paths []string, have map[string]bool) string {
	if !p.Smart || len(p.Words) > 16 {
		return ""
	}
	so := o.so
	so.Partial = true
	q, err := search.Compile(defsExpr(o.pattern), so)
	if err != nil {
		return ""
	}
	q.DefMode = true
	found, _ := search.Run(paths, q)
	var parts []string
	n := 0
	for _, g := range buildAll(found, q) {
		for _, d := range g.Defs {
			name := lastPart(d.Name)
			hit := false
			for _, w := range p.Words {
				if strings.Contains(strings.ToLower(name), strings.ToLower(lastPart(w))) {
					hit = true
				}
			}
			if !hit || have[g.Path+":"+strconv.Itoa(d.Line)] {
				continue
			}
			n++
			if len(parts) < 6 {
				parts = append(parts, fmt.Sprintf("%s (%s:%d)", name, g.Path, d.Line))
			}
		}
	}
	if n == 0 {
		return ""
	}
	if n > len(parts) {
		parts = append(parts, fmt.Sprintf("+%d more", n-len(parts)))
	}
	s := strings.Join(parts, ", ")
	if cmd := command("-d --partial ", o.pattern); cmd != "" {
		s += ": " + cmd
	}
	return s
}

// onlyDefs is f with just the hits that are the given definitions.
func onlyDefs(f *group.File, defs []group.Def) *group.File {
	lines := map[int]bool{}
	for _, d := range defs {
		lines[d.Line] = true
	}
	c := *f
	c.Defs = defs
	c.Groups = nil
	for _, g := range f.Groups {
		var hs []group.Hit
		for _, h := range g.Hits {
			if lines[h.Line] {
				hs = append(hs, h)
			}
		}
		if len(hs) > 0 {
			ng := *g
			ng.Hits = hs
			c.Groups = append(c.Groups, &ng)
		}
	}
	return &c
}

// hints say what would have matched: other casing, longer words, similar names.
func hints(out *bufio.Writer, o options, p *search.Pattern, paths []string) {
	count := func(expr string, so search.Options) (int, int, []string) {
		q, err := search.Compile(expr, so)
		if err != nil {
			return 0, 0, nil
		}
		found, _ := search.Run(paths, q)
		hits := 0
		seen := map[string]bool{}
		var words []string
		for _, f := range found {
			hits += len(f.Hits)
			for _, h := range f.Hits {
				for _, s := range h.Spans {
					w := wordAt(h.Text, s[0], s[1])
					if !seen[w] && len(words) < 4 {
						seen[w] = true
						words = append(words, w)
					}
				}
			}
		}
		return hits, len(found), words
	}
	if !p.Fold {
		so := o.so
		so.IgnoreCase = true
		if n, nf, ws := count(o.pattern, so); n > 0 {
			fmt.Fprintf(out, "  ignoring case: %s in %s (%s): seek -i %s\n", plural(n, "hit"), plural(nf, "file"), strings.Join(ws, ", "), shellQuote(o.pattern))
		}
	}
	if p.Smart && len(p.Words) <= 16 {
		so := o.so
		so.Partial = true
		so.IgnoreCase = so.IgnoreCase || false
		if n, nf, ws := count(o.pattern, so); n > 0 {
			fmt.Fprintf(out, "  inside longer words: %s in %s (%s): seek --partial %s\n", plural(n, "hit"), plural(nf, "file"), strings.Join(ws, ", "), shellQuote(o.pattern))
		}
	}
	if len(p.Names) == 0 {
		return
	}
	names := suggest.Similar(p.Names, paths, 20, p.Fold)
	if len(names) == 0 {
		return
	}
	// among equally similar names, defined ones first
	type cand struct {
		n     suggest.Name
		def   string
		score int
	}
	var cs []cand
	for _, n := range names {
		cs = append(cs, cand{n: n, def: firstDef(n.Word, n.Paths), score: n.Score()})
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].score != cs[j].score {
			return cs[i].score < cs[j].score
		}
		return cs[i].def != "" && cs[j].def == ""
	})
	// the closest few: a typo's fix, not a list of everything sharing a part
	keep := cs[:0]
	for _, c := range cs {
		if c.score <= cs[0].score+2 && len(keep) < 6 {
			keep = append(keep, c)
		}
	}
	fmt.Fprintln(out, "  similar names:")
	for _, c := range keep {
		n := c.n
		where := n.Paths[0]
		if c.def != "" {
			where = "defined at " + c.def
		} else if len(n.Paths) > 1 {
			where = fmt.Sprintf("in %s, e.g. %s", plural(len(n.Paths), "file"), n.Paths[0])
		}
		fmt.Fprintf(out, "    %s  %s, %s\n", n.Word, plural(n.Count, "hit"), where)
	}
}

// wordAt widens a match to the identifier around it.
func wordAt(text string, a, b int) string {
	isW := func(r rune) bool { return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r) }
	for a > 0 {
		r, n := utf8.DecodeLastRuneInString(text[:a])
		if !isW(r) {
			break
		}
		a -= n
	}
	for b < len(text) {
		r, n := utf8.DecodeRuneInString(text[b:])
		if !isW(r) {
			break
		}
		b += n
	}
	return text[a:b]
}

// firstDef is where word is defined ("src/a.js:12 (function load)"), if
// the outline rules or a defining keyword show it in one of paths.
func firstDef(word string, paths []string) string {
	p, err := search.Compile(word, search.Options{})
	if err != nil {
		return ""
	}
	found, _ := search.Run(paths[:min(len(paths), 30)], p)
	for _, f := range found {
		g := group.Build(f, p)
		for _, d := range g.Defs {
			if lastPart(d.Name) == word {
				return fmt.Sprintf("%s:%d (%s)", f.Path, d.Line, d.Kind)
			}
		}
	}
	return ""
}

func lastPart(name string) string {
	if i := strings.LastIndexAny(name, ".:"); i >= 0 {
		return name[i+1:]
	}
	return name
}

func shellQuote(s string) string {
	for _, c := range s {
		if !(c == '_' || c == '.' || c == '-' || c == '/' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	if strings.HasSuffix(word, "y") {
		return fmt.Sprintf("%d %sies", n, word[:len(word)-1])
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func parse(args []string) (options, int, string) {
	o := options{}
	var rest []string
	value := func(i *int, name string) (string, string) {
		if *i+1 >= len(args) {
			return "", name + " needs a value"
		}
		*i++
		return args[*i], ""
	}
	number := func(s, name string) (int, string) {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return 0, name + " needs a number, not " + strconv.Quote(s)
		}
		return n, ""
	}
	setPattern := func(s string) string {
		if o.havePattern {
			return "one pattern only (join several with |: 'a|b')"
		}
		o.pattern, o.havePattern = s, true
		return ""
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		var msg string
		switch {
		case a == "--":
			rest = append(rest, args[i+1:]...)
			i = len(args)
		case a == "-h" || a == "--help":
			return o, 0, usage
		case a == "--version":
			return o, 0, "seek " + version + "\n"
		case a == "--partial":
			o.so.Partial = true
		case a == "-e":
			var s string
			if s, msg = value(&i, "-e"); msg == "" {
				msg = setPattern(s)
			}
		case a == "-C" || a == "-b" || a == "-g":
			var s string
			if s, msg = value(&i, a); msg == "" {
				msg = o.setValue(a[1], s, number)
			}
		case strings.HasPrefix(a, "--"):
			msg = "unknown flag " + a + " (seek -h lists them)"
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// combined short flags: -is, -C3, -b50, -g*.js
			for k := 1; k < len(a) && msg == ""; k++ {
				switch c := a[k]; c {
				case 'd':
					o.defs = true
				case 's':
					o.bodies = true
				case 'c':
					o.counts = true
				case 'l':
					o.list = true
				case 'i':
					o.so.IgnoreCase = true
				case 'F':
					o.so.Fixed = true
				case 'w':
					o.so.Words = true
				case 'u':
					o.unrestrict = true
				case 'A':
					o.plain = true
				case '0':
					o.null = true
				case 'C', 'b', 'g', 'e':
					v := a[k+1:]
					if v == "" {
						if v, msg = value(&i, "-"+string(c)); msg != "" {
							break
						}
					}
					if c == 'e' {
						msg = setPattern(v)
					} else {
						msg = o.setValue(c, v, number)
					}
					k = len(a)
				default:
					msg = "unknown flag -" + string(c) + " (seek -h lists them)"
					if len(a) > 2 && !o.havePattern && len(rest) == 0 {
						msg = fmt.Sprintf("unknown flag -%c in %q; a pattern that starts with - goes after -e or --: seek -e %s", c, a, a)
					}
				}
			}
		default:
			rest = append(rest, a)
		}
		if msg != "" {
			return o, 2, msg
		}
	}
	if !o.havePattern {
		if len(rest) == 0 {
			return o, 2, "what should I search for? usage: seek [flags] PATTERN [PATH...] (seek -h for more)"
		}
		o.pattern, rest = rest[0], rest[1:]
	}
	if o.pattern == "" {
		return o, 2, "the pattern is empty"
	}
	for _, p := range rest {
		if _, err := os.Stat(p); err != nil {
			return o, 2, "no such file or folder: " + p
		}
		o.paths = append(o.paths, filepath.Clean(p))
	}
	if o.null && !o.list {
		return o, 2, "-0 separates the paths of -l with NUL bytes; use it with -l"
	}
	if o.list && (o.bodies || o.counts || o.defs) {
		return o, 2, "-l prints only paths; it doesn't combine with -s, -c or -d"
	}
	if o.bodies && o.counts {
		return o, 2, "-s shows whole definitions and -c only counts; pick one"
	}
	return o, 0, ""
}

func (o *options) setValue(flag byte, v string, number func(string, string) (int, string)) string {
	var msg string
	switch flag {
	case 'C':
		o.context, msg = number(v, "-C")
	case 'b':
		o.budget, msg = number(v, "-b")
		o.budgetSet = true
	case 'g':
		o.globs = append(o.globs, v)
	}
	return msg
}
