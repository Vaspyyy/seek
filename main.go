// seek is a code search for people and AI coding agents: hits grouped by
// the function, class or section that contains them, labelled as
// definition, assignment, import, comment and so on, within a line budget.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Vaspyyy/prism/repo"
	"github.com/Vaspyyy/seek/internal/files"
	"github.com/Vaspyyy/seek/internal/group"
	"github.com/Vaspyyy/seek/internal/render"
	"github.com/Vaspyyy/seek/internal/search"
	"github.com/Vaspyyy/seek/internal/suggest"
	"golang.org/x/term"
)

const version = "0.2.0"

const usage = `seek: search code, with hits grouped by the definition that contains them

usage: seek [flags] PATTERN [PATH...]

A pattern is a regular expression (Go syntax). One that is an identifier,
or several joined by | ("setSpeed", "Repo.Open", "a|b|c"), matches whole
words only. An invalid regex is searched as plain text.

A pattern that is a ref to a file seek can find ("internal/files/files.go:List",
"files.go:globSet.match", "main.go:104") shows that definition (for a line:
the innermost one containing it), then the hits of its name elsewhere,
grouped as usual. With -F or -e the pattern is always a pattern.

  -d          definitions only: where PATTERN is defined, one line each
  -s          show the whole definition around each hit (windows in long ones)
  -c          counts: at most one line per definition
  -l          only the paths of files with hits (-0: separated by NUL bytes;
              with -d, the files that define it)
  -C N        N lines of context around each hit (with -s: the window size)
  -i          ignore case
  -F          plain text, not a regex
  -w          whole words, also for a regex
  --partial   identifiers also inside longer words (speed finds speedMult)
  --writes    only hits that set the name: definitions, declarations, assignments
  -g GLOB     only files matching GLOB ("*.js", "src/**", "!*_test.go"); repeatable
  -u          also ignored files, caches and dot-folders
  -b N        about how many lines to print (default 100, with -s 300; 0: no limit)
  -e PATTERN  a pattern that starts with "-" (or that isn't a ref)
  -A          plain output without colors (the default when not on a terminal)
  --version

Files are what git sees in a repository (tracked and untracked, not
ignored); elsewhere caches, build output and dot-folders are skipped.
Paths are printed relative to the repository root.
Exit status: 0 if something matched, 1 if not, 2 on errors.
`

type options struct {
	pattern                    string
	havePattern                bool
	patternFlag                bool // the pattern came with -e: never a ref
	paths, globs               []string
	defs, bodies, counts, list bool
	context, budget            int
	budgetSet                  bool
	so                         search.Options
	plain, unrestrict, null    bool
	writes                     bool
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
	shown = filepath.ToSlash
	detected := detect()
	var info *repo.Info
	if refLike(o) {
		info = detected()
	}
	t, isRef := asRef(o, info)
	roots := o.paths
	if isRef && len(roots) == 0 {
		roots = []string{rootDir(info)} // a ref's callers anywhere in the repository
	}
	paths, err := files.List(roots, files.Options{Globs: o.globs, Unrestrict: o.unrestrict})
	if err != nil {
		fmt.Fprintln(errw, "seek: "+err.Error())
		return 2
	}
	if isRef {
		return refSearch(out, errw, o, info, t, paths, color)
	}
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
	found, st := search.Run(paths, p)
	detected() // before anything is printed: it sets how paths are shown
	gs := buildAll(found, p)
	allHits := 0 // before --writes
	if o.writes {
		allHits = countHits(gs)
		gs = writesOnly(gs)
	}
	if o.list && o.defs {
		gs = definingFiles(o, p, gs)
	}
	if o.list {
		sort.Slice(gs, func(i, j int) bool { return gs[i].Path < gs[j].Path })
		// notes go to stderr, so the paths can be piped
		if len(gs) == 0 {
			fmt.Fprintf(errw, "seek: %s: no hits (without -l, seek suggests what would match)\n", describe(p))
			ew := bufio.NewWriter(errw)
			skipped(ew, o, st, true)
			ew.Flush()
		} else if m := missed(o, p, paths, countHits(gs)); m.lines > 0 && !o.defs {
			fmt.Fprintln(errw, "seek: "+m.note(o.pattern))
		}
		listPaths(out, gs, o.null)
		return exitCode(len(gs) > 0)
	}
	render.Order(gs)
	budget := o.budget
	if o.budgetSet && budget == 0 {
		budget = 1 << 30 // -b 0: no limit
	}
	if !o.budgetSet {
		budget = 100
		if o.bodies {
			budget = 300
		}
	}
	ro := render.Options{Budget: budget - 1, Color: color, Context: o.context, Bodies: o.bodies, Counts: o.counts, Path: shown}
	scope := scopeOf(o, st)
	if o.defs {
		return definitions(out, o, p, gs, scope, ro, paths, searched)
	}
	hits := 0
	for _, f := range gs {
		hits += f.Count()
	}
	if hits == 0 && allHits > 0 {
		fmt.Fprintf(out, "seek: %s in %s: no hits that set it (%s: %s)\n", describe(p), scope, plural(allHits, "other hit"), command("", o.pattern))
		return 1
	}
	if hits == 0 {
		fmt.Fprintf(out, "seek: %s in %s: no hits\n", describe(p), scope)
		if d := qualifiedDef(o, p, paths); d != "" {
			fmt.Fprintf(out, "  defined at %s: %s\n", d, command("-d ", o.pattern))
		}
		skipped(out, o, st, true)
		hints(out, o, p, paths)
		return 1
	}
	count := plural(hits, "hit")
	if o.writes {
		count += " that set it"
	}
	head := fmt.Sprintf("seek: %s: %s in %s", describe(p), count, plural(len(gs), "file"))
	if len(o.paths) > 0 || len(o.globs) > 0 || st.TooBig > 0 {
		head = fmt.Sprintf("seek: %s in %s: %s in %s", describe(p), scope, count, plural(len(gs), "file"))
	}
	layout := render.Plan(gs, ro)
	if d := defsLine(gs, 3, p, layout); d != "" {
		head += "; defined at " + d
	}
	fmt.Fprintln(out, head)
	if o.writes {
		if n := allHits - hits; n > 0 {
			fmt.Fprintf(out, "+%s that don't set it: %s\n", plural(n, "more hit"), command("", o.pattern))
		}
	} else if m := missed(o, p, paths, hits); m.lines > 0 {
		fmt.Fprintln(out, m.note(o.pattern)) // first, where it can't be overlooked
	}
	render.Print(out, gs, layout, ro)
	notes(out, layout, gs, ro)
	skipped(out, o, st, false)
	return 0
}

// detect finds the repository seek runs in, in the background: that takes
// a few git commands, which run while seek lists and searches the files.
// The function it returns waits for the result and sets shown.
func detect() func() *repo.Info {
	ch := make(chan *repo.Info, 1)
	dir, err := os.Getwd()
	go func() {
		var in *repo.Info
		if err == nil {
			in, _ = repo.Detect(dir)
		}
		ch <- in
	}()
	var in *repo.Info
	done := false
	return func() *repo.Info {
		if !done {
			in, done = <-ch, true
			if in != nil {
				shown = printedPaths(in)
			}
		}
		return in
	}
}

// shown turns a path as seek found it (relative to the working directory,
// or as given) into the form it prints: relative to the repository root,
// with forward slashes, also when seek runs in a subdirectory, so that a
// path or ref seek prints works in every tool. Outside a repository the
// working directory is the root. Set by run; -l prints paths as found,
// for the commands they're piped to.
var shown = filepath.ToSlash

func printedPaths(in *repo.Info) func(string) string {
	base, ok := in.Rel(".")
	if !ok {
		return filepath.ToSlash
	}
	var show func(string) string
	show = func(p string) string {
		if dir, ok := strings.CutSuffix(p, "/"); ok && dir != "" {
			return show(dir) + "/" // a folder
		}
		if !filepath.IsAbs(p) {
			if s := path.Join(base, filepath.ToSlash(p)); s != ".." && !strings.HasPrefix(s, "../") {
				return s
			}
		}
		if r, ok := in.Rel(p); ok {
			return r
		}
		return filepath.ToSlash(p) // outside the repository: as given
	}
	return show
}

// rootDir is the repository's root as a path from the working directory:
// "." or "../..".
func rootDir(in *repo.Info) string {
	base, ok := in.Rel(".")
	if !ok || base == "." {
		return "."
	}
	return strings.TrimSuffix(strings.Repeat("../", strings.Count(base, "/")+1), "/")
}

// listPaths prints the paths of files for -l, as they were found (relative
// to the working directory), for the command they're piped to.
func listPaths(out *bufio.Writer, gs []*group.File, null bool) {
	end := "\n"
	if null {
		end = "\x00"
	}
	for _, f := range gs {
		path := f.Path
		if strings.HasPrefix(path, "-") {
			path = "./" + path // not an option to the command it's piped to
		}
		fmt.Fprint(out, path+end)
	}
}

// skipped says which files named on the command line weren't searched
// (binary, too big), and, when nothing was found, the binary files, ignored
// folders and nested repositories that weren't searched.
func skipped(out *bufio.Writer, o options, st search.Stats, none bool) {
	named := map[string]bool{}
	for _, path := range o.paths {
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			if _, why := search.Read(path); why != "" {
				fmt.Fprintf(out, "  not searched: %s is %s\n", shown(path), why)
				named[path] = true
			}
		}
	}
	var binary []string
	for _, path := range st.BinaryPaths {
		if !named[path] {
			binary = append(binary, shown(path))
		}
	}
	if none && len(binary) > 0 {
		list := strings.Join(binary[:min(len(binary), 3)], ", ")
		if len(binary) > 3 {
			list += fmt.Sprintf(", +%d more", len(binary)-3)
		}
		fmt.Fprintf(out, "  not searched: %s (%s)\n", plural(len(binary), "binary file"), list)
	}
	if none && !o.unrestrict {
		if sk := files.Skipped(6); len(sk) > 0 {
			more := ""
			if len(sk) == 6 {
				sk, more = sk[:5], ", …"
			}
			for i := range sk {
				sk[i] = shown(sk[i])
			}
			fmt.Fprintf(out, "  not searched: %s%s (ignored by git, caches or dot-folders; -u searches them)\n", strings.Join(sk, ", "), more)
		}
	}
	if n := len(files.Nested); n > 0 && none {
		var names []string
		for _, p := range files.Nested[:min(n, 4)] {
			names = append(names, shown(p))
		}
		more := ""
		if n > len(names) {
			more = fmt.Sprintf(" +%d more", n-len(names))
		}
		fmt.Fprintf(out, "  not searched: %s inside: %s%s (give it as a path to search it)\n", plural(n, "repository"), strings.Join(names, ", "), more)
	}
}

// notes end the output with what the budget left out of -s and -C.
func notes(out *bufio.Writer, l *render.Layout, gs []*group.File, ro render.Options) {
	if reduced, total := l.Reduced(gs, ro); reduced > 0 {
		flag, what := "-s", "show only part of their definitions"
		if !ro.Bodies {
			flag, what = "-C", "show less context around their hits"
		}
		if reduced == 1 {
			what = strings.Replace(strings.Replace(what, "show ", "shows ", 1), "their", "its", 1)
		}
		fmt.Fprintf(out, "(%s: %d of %s %s to fit the budget; raise -b, e.g. -b %d, or -b 0 for no limit)\n",
			flag, reduced, plural(total, "file"), what, max(ro.Budget*2, 300))
	}
}

// missed is what a whole-word, case-sensitive search left out: the lines
// that match only in another case or inside longer words, the most
// frequent of those words, and the flags that would include them.
type missedHits struct {
	lines        int
	words, flags string
	what         string // "in other case", "inside longer words" or both
}

func missed(o options, p *search.Pattern, paths []string, wholeLines int) missedHits {
	if !p.Smart || len(p.Words) > 16 {
		return missedHits{}
	}
	so := o.so
	so.Partial, so.IgnoreCase = true, true
	q, err := search.Compile(o.pattern, so)
	if err != nil {
		return missedHits{}
	}
	found, _ := search.Run(paths, q)
	lines := 0
	counts := map[string]int{}
	// a word the search itself matches whole (limit for lim.t, LIMIT with
	// -i) is a hit already, not a missed one
	exact := func(w string) bool {
		loc := p.Re.FindStringIndex(w)
		return loc != nil && loc[0] == 0 && loc[1] == len(w)
	}
	longer, cased := false, false
	for _, f := range found {
		lines += len(f.Hits)
		for _, h := range f.Hits {
			seen := map[string]bool{}
			for _, s := range h.Spans {
				w := wordAt(h.Text, s[0], s[1])
				if exact(w) || seen[w] {
					continue
				}
				seen[w] = true
				counts[clipWord(w)]++
				if len(w) > s[1]-s[0] {
					longer = true
				}
				if !exact(h.Text[s[0]:s[1]]) {
					cased = true
				}
			}
		}
	}
	extra := lines - wholeLines
	if extra <= 0 || len(counts) == 0 {
		return missedHits{}
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
	m := missedHits{lines: extra, words: strings.Join(parts, ", ")}
	switch {
	case cased && longer:
		m.what, m.flags = "in another case or inside longer words", "-i --partial "
	case cased:
		m.what, m.flags = "in another case", "-i "
	case p.Fold:
		m.what, m.flags = "inside longer words", "-i --partial "
	default:
		m.what, m.flags = "inside longer words", "--partial "
	}
	return m
}

func (m missedHits) note(pattern string) string {
	s := fmt.Sprintf("+%s %s (%s)", plural(m.lines, "more hit"), m.what, m.words)
	if cmd := command(m.flags, pattern); cmd != "" {
		s += ": " + cmd
	}
	return s
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

// writesOnly keeps the hits that set the name: definitions, declarations
// and assignments.
func writesOnly(gs []*group.File) []*group.File {
	var out []*group.File
	for _, f := range gs {
		c := *f
		c.Groups = nil
		for _, g := range f.Groups {
			var hs []group.Hit
			for _, h := range g.Hits {
				if (h.Label == "definition" || h.Label == "declaration" || h.Label == "assignment") && !readsOnly(h) {
					hs = append(hs, h)
				}
			}
			if len(hs) > 0 {
				ng := *g
				ng.Hits = hs
				c.Groups = append(c.Groups, &ng)
			}
		}
		if len(c.Groups) > 0 {
			out = append(out, &c)
		}
	}
	return out
}

// getter matches a JavaScript getter's first line, up to its name.
var getter = regexp.MustCompile(`^\s*(?:static\s+)?get\s+$`)

// readsOnly: a definition or declaration hit that reads the name rather
// than setting it: a getter (get x()) or a destructuring key
// (const { x: alias } = data, which declares alias).
func readsOnly(h group.Hit) bool {
	if len(h.Spans) == 0 {
		return false
	}
	before, after := h.Text[:h.Spans[0][0]], strings.TrimSpace(h.Text[h.Spans[0][1]:])
	if h.Label == "definition" && getter.MatchString(before) {
		return true
	}
	return h.Label == "declaration" && strings.Contains(before, "{") &&
		strings.HasPrefix(after, ":") && !strings.HasPrefix(after, "::")
}

func countHits(gs []*group.File) int {
	n := 0
	for _, f := range gs {
		n += f.Count()
	}
	return n
}

// definingFiles keeps the files that define the pattern (for -l -d).
func definingFiles(o options, p *search.Pattern, gs []*group.File) []*group.File {
	want := strings.Split(o.pattern, "|")
	var out []*group.File
	for _, f := range gs {
		for _, d := range f.Defs {
			name := d.Name
			if t, typ, ok := strings.Cut(name, " for "); ok && (keepName(t, want, p.Fold) || keepName(typ, want, p.Fold)) ||
				!p.Smart || keepName(name, want, p.Fold) {
				out = append(out, f)
				break
			}
		}
	}
	return out
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
	case p.Smart && p.Text:
		notes = append(notes, "text, whole word")
	case p.Smart && len(p.Words) > 1:
		notes = append(notes, "whole words")
	case p.Smart:
		notes = append(notes, "whole word")
	case p.Text:
		notes = append(notes, "text")
	case len(p.Words) > 0:
		notes = append(notes, "also inside longer words")
	}
	if p.Empty {
		notes = append(notes, "matches the empty string, so every line")
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
			return shown(o.paths[0])
		}
	}
	n := plural(st.Files, "file")
	where := ""
	if len(o.paths) > 0 {
		var ps []string
		for _, p := range o.paths {
			ps = append(ps, shown(p))
		}
		where = strings.Join(ps, " ") + ", "
	}
	if len(o.globs) > 0 {
		n = fmt.Sprintf("%s matching %s", plural(st.Files, "file"), strings.Join(o.globs, " "))
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
				s := fmt.Sprintf("%s:%d", shown(f.Path), d.Line)
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
		// impl Shape for Grid defines Shape for Grid: it answers -d Shape
		if trait, typ, ok := strings.Cut(name, " for "); ok {
			if keepName(trait, want, p.Fold) || keepName(typ, want, p.Fold) {
				return true
			}
		}
		if i := strings.IndexByte(name, '<'); i > 0 {
			name = name[:i] // impl Stack<T>: Stack
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
		if outlined(a) != outlined(b) {
			return outlined(a)
		}
		return ownDef(a) && !ownDef(b) // the trait or type itself, then its impls
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
			fmt.Fprintln(out, "  definitions whose names contain it: "+c)
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
			fmt.Fprintln(out, "definitions whose names contain it: "+more)
		}
		return 0
	}
	printed := 0
	for _, f := range defFiles {
		for _, d := range f.Defs {
			if printed >= max(ro.Budget, 1) {
				fmt.Fprintf(out, "… %s (raise -b)\n", plural(total-printed, "more definition"))
				return 0
			}
			kind := ""
			if f.Kind != "" {
				kind = "  (" + f.Kind + ")"
			}
			if d.Outline {
				fmt.Fprintln(out, defLine(shown(f.Path), d.RefName(), d.Line, d.End, d.Sig)+kind)
			} else {
				// a field, a constant, a keyword's name: a line, which other
				// tools resolve to the definition containing it
				fmt.Fprintf(out, "%s:%d  %s%s\n", shown(f.Path), d.Line, d.Sig, kind)
			}
			printed++
		}
	}
	if more != "" {
		fmt.Fprintln(out, "definitions whose names contain it: "+more)
	}
	return 0
}

// defLine is a definition as -d prints it: its ref, its lines from the
// defining one, and its signature:
// "internal/files/files.go:List  L73-107  func List(roots []string, o Options) ([]string, error)".
func defLine(path, name string, line, end int, sig string) string {
	rng := "L" + strconv.Itoa(line)
	if end > line {
		rng += "-" + strconv.Itoa(end)
	}
	return path + ":" + name + "  " + rng + "  " + sig
}

// keepName: name (possibly qualified, possibly with generics) is one of want.
func keepName(name string, want []string, fold bool) bool {
	if i := strings.IndexByte(name, '<'); i > 0 {
		name = name[:i]
	}
	name = strings.TrimSpace(name)
	for _, w := range want {
		if lastPart(name) == lastPart(w) || fold && strings.EqualFold(lastPart(name), lastPart(w)) {
			return true
		}
	}
	return false
}

func ownDef(f *group.File) bool {
	for _, d := range f.Defs {
		if d.Kind != "impl" {
			return true
		}
	}
	return false
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
				if d.Outline {
					parts = append(parts, shown(g.Path)+":"+d.RefName())
				} else {
					parts = append(parts, fmt.Sprintf("%s:%d (%s)", shown(g.Path), d.Line, name))
				}
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
					w := clipWord(wordAt(h.Text, s[0], s[1]))
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
		where := fmt.Sprintf("%s:%d", shown(n.Paths[0]), n.Line)
		if c.def != "" {
			where = "defined at " + c.def
		} else if len(n.Paths) > 1 {
			where = fmt.Sprintf("in %s, e.g. %s:%d", plural(len(n.Paths), "file"), shown(n.Paths[0]), n.Line)
		}
		fmt.Fprintf(out, "    %s  %s, %s\n", clipWord(n.Word), plural(n.Count, "hit"), where)
	}
}

// wordAt widens a match to the identifier around it.
func wordAt(text string, a, b int) string {
	isW := func(r rune) bool { return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r) }
	for a > 0 {
		r, n := utf8.DecodeLastRuneInString(text[:a])
		if r == utf8.RuneError && n == 1 {
			r = rune(text[a-1]) // not UTF-8: a Latin-1 byte
		}
		if !isW(r) {
			break
		}
		a -= n
	}
	for b < len(text) {
		r, n := utf8.DecodeRuneInString(text[b:])
		if r == utf8.RuneError && n == 1 {
			r = rune(text[b])
		}
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
				return fmt.Sprintf("%s:%d (%s)", shown(f.Path), d.Line, d.Kind)
			}
		}
	}
	return ""
}

// qualifiedDef is where a qualified name (Repo.Changes) is defined when its
// text never appears as such: func (r *Repo) Changes defines it.
func qualifiedDef(o options, p *search.Pattern, paths []string) string {
	if !p.Smart || len(p.Words) != 1 || !strings.Contains(p.Words[0], ".") {
		return ""
	}
	name := p.Words[0]
	so := o.so
	so.Partial = false
	q, err := search.Compile(lastPart(name), so)
	if err != nil {
		return ""
	}
	q.DefMode = true
	found, _ := search.Run(paths, q)
	for _, f := range found {
		for _, d := range group.Build(f, q).Defs {
			if d.Name == name || strings.HasSuffix(d.Name, "."+name) {
				return fmt.Sprintf("%s:%d (%s)", shown(f.Path), d.Line, d.Kind)
			}
		}
	}
	return ""
}

// clipWord shortens a word for a note (a minified file's "word" can be
// thousands of characters long).
func clipWord(w string) string {
	if r := []rune(w); len(r) > 40 {
		return string(r[:39]) + "…"
	}
	return w
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
		case a == "--writes":
			o.writes = true
		case a == "-e":
			var s string
			if s, msg = value(&i, "-e"); msg == "" {
				msg = setPattern(s)
			}
			o.patternFlag = true
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
						o.patternFlag = true
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
	for k, p := range rest {
		if _, err := os.Stat(p); err != nil {
			msg := "no such file or folder: " + p
			if k == 0 && !strings.ContainsAny(p, "/.") {
				msg += fmt.Sprintf(" (to search for several words, join them: '%s|%s')", o.pattern, p)
			}
			return o, 2, msg
		}
		o.paths = append(o.paths, filepath.Clean(p))
	}
	if o.null && !o.list {
		return o, 2, "-0 separates the paths of -l with NUL bytes; use it with -l"
	}
	if o.list && (o.bodies || o.counts) {
		return o, 2, "-l prints only paths; it doesn't combine with -s or -c"
	}
	if o.bodies && o.counts {
		return o, 2, "-s shows whole definitions and -c only counts; pick one"
	}
	if o.defs && o.counts {
		return o, 2, "-d lists definitions one line each and -c counts hits; pick one"
	}
	if o.defs && o.context > 0 && !o.bodies {
		return o, 2, "-d lists definitions one line each; -d -s shows their code"
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
