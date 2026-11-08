package main

// A ref as PATTERN ("internal/files/files.go:List", "files.go:match",
// "main.go:104") shows that definition, then the hits of its name
// elsewhere: its callers and mentions. The format is Prism's (docs/refs.md),
// shared with glint, recap and crux.

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Vaspyyy/prism/outline"
	"github.com/Vaspyyy/prism/ref"
	"github.com/Vaspyyy/prism/repo"
	"github.com/Vaspyyy/seek/internal/files"
	"github.com/Vaspyyy/seek/internal/group"
	"github.com/Vaspyyy/seek/internal/render"
	"github.com/Vaspyyy/seek/internal/search"
)

// refPattern is a pattern read as a ref: the ref and the files its path
// names (repo-relative; several when a path suffix fits more than one).
type refPattern struct {
	r     ref.Ref
	files []string
	// shadowed is the reading of the path from the working directory, when
	// that file exists too but the one from the repository root won
	shadowed string
}

// source is a file a ref names, with its symbols' place in the targets.
type source struct {
	rel  string // repo-relative
	data []byte
	at   int // index of its first target
}

// asRef reads the pattern as a ref when it names a symbol or a line in a
// file that exists: by its path from the repository root or the working
// directory, or by the end of a path in the repository ("files.go"). It
// runs before the files to search are listed, since listing the
// repository's files for the latter resets what files.Skipped reports.
// Anything else is a regex, as always, so text like "foo:bar" is still
// searched; -F and -e always mean a pattern.
func asRef(o options, info *repo.Info) (refPattern, bool) {
	if info == nil || !refLike(o) {
		return refPattern{}, false
	}
	r := ref.Parse(o.pattern, info.IsRev)
	if r.Path == "" || r.Name == "" && !r.IsLocation() {
		return refPattern{}, false // ":x", "TODO:"
	}
	if res, err := info.Resolve(r.Path, "."); err == nil && res.Exists {
		if fi, err := os.Stat(info.Abs(res.Rel)); err == nil && fi.Mode().IsRegular() {
			return refPattern{r: r, files: []string{res.Rel}, shadowed: res.Shadowed}, true
		}
	}
	// the end of a path: only for what looks like one, so that text such as
	// "std::string" costs no listing of the repository
	if !strings.ContainsAny(r.Path, "./") || strings.ContainsAny(r.Path, " \t") {
		return refPattern{}, false
	}
	all, err := files.List([]string{info.Root}, files.Options{Unrestrict: o.unrestrict})
	if err != nil {
		return refPattern{}, false
	}
	for i, p := range all {
		all[i] = shown(p)
	}
	if m := info.Match(r.Path, ".", all); len(m) > 0 {
		return refPattern{r: r, files: m}, true
	}
	return refPattern{}, false
}

// refLike: the pattern may be a ref, which has a ':' after its path. -F and
// -e always mean a pattern.
func refLike(o options) bool {
	return !o.so.Fixed && !o.patternFlag && strings.Contains(o.pattern, ":")
}

// refSearch prints what a ref names: the definition, as -s shows one (doc
// comment included), then the hits of its name elsewhere, grouped by the
// definitions that contain them, without the definition itself. A line
// stands for the innermost definition containing it.
func refSearch(out *bufio.Writer, errw io.Writer, o options, info *repo.Info, rp refPattern, paths []string, color bool) int {
	r := rp.r
	typed := strings.TrimSpace(o.pattern)
	if r.Rev != "" {
		at := r
		at.Rev = ""
		if len(rp.files) == 1 {
			at.Path = rp.files[0]
		}
		fmt.Fprintf(errw, "seek: %s names commit %s, but seek searches the working tree; for that version use: recap show %s --show %s\n",
			typed, r.Rev, shellQuote(r.Rev), shellQuote(at.String()))
		return 2
	}
	if r.IsLocation() && len(rp.files) > 1 {
		// a line number means one file: say which ones the path could be
		var locs []string
		for _, f := range rp.files {
			loc := r
			loc.Path = f
			locs = append(locs, loc.String())
		}
		fmt.Fprintf(out, "seek: %s: %s end in %s; name one: %s\n", typed, plural(len(rp.files), "path"), r.Path, capped(locs, 8))
		return 1
	}
	srcs, ts, why := readSources(info, rp.files)
	if why != "" {
		fmt.Fprintln(errw, "seek: "+why)
		return 2
	}
	m := ref.Match(r, ts, rp.files)
	if len(m) == 0 && r.IsLocation() {
		return outsideDefs(out, o, r, srcs, color)
	}
	if len(m) == 0 {
		return noDefinition(out, typed, r, ts, rp.files)
	}

	word, so, p, err := namePattern(o, ts[m[0]])
	if err != nil {
		fmt.Fprintln(errw, "seek: "+err.Error())
		return 2
	}
	if o.list {
		gs, _ := refFiles(paths, p, ts, m, o.defs)
		listPaths(out, gs, o.null)
		return exitCode(len(gs) > 0)
	}
	defFiles, inDefs, _ := refDefs(info, srcs, ts, m, p)
	budget := 1 << 30
	if o.budgetSet && o.budget > 0 {
		budget = o.budget
	} else if !o.budgetSet {
		budget = 300 // the definitions, as -s shows them
	}
	roDefs := render.Options{Budget: budget - 1, Color: color, Bodies: true, Path: shown}
	refs := ref.Refs(ts)
	if o.defs {
		fmt.Fprintf(out, "seek -d: %s: %s\n", typed, plural(len(m), "definition"))
		if o.bodies {
			render.Files(out, defFiles, roDefs)
			return 0
		}
		for _, i := range m {
			t := ts[i]
			fmt.Fprintln(out, defLine(t.Path, refName(refs[i]), t.Line, t.End, sigOf(defFiles, t)))
		}
		return 0
	}
	var defsOut bytes.Buffer
	layout := render.Plan(defFiles, roDefs)
	render.Print(&defsOut, defFiles, layout, roDefs)
	reduced, _ := layout.Reduced(defFiles, roDefs)

	rest, st := elsewhere(paths, p, inDefs, o.writes)
	hits := countHits(rest)

	describe := func(i int) string {
		t := ts[i]
		rng := "L" + strconv.Itoa(t.Line)
		if t.End > t.Line {
			rng += "-" + strconv.Itoa(t.End)
		}
		return fmt.Sprintf("%s (%s, %s)", refs[i], t.Kind, rng)
	}
	var ds []string
	for _, i := range m {
		ds = append(ds, describe(i))
	}
	what, sep := strings.Join(ds, ", "), ", "
	if len(m) > 3 {
		what = plural(len(m), "definition") // shown below
	} else if len(m) > 1 {
		sep = "; " // after a list
	}
	switch {
	case r.IsLocation():
		what = typed + " is in " + what
	case len(m) > 3:
		what = typed + " matches " + what
	case len(m) > 1:
		what = fmt.Sprintf("%s matches %s: %s", typed, plural(len(m), "definition"), what)
	}
	q := `"` + clipWord(word) + `"`
	var how []string
	if so.IgnoreCase {
		how = append(how, "ignoring case")
	}
	if so.Partial && len(p.Words) > 0 {
		how = append(how, "also inside longer words")
	}
	if len(how) > 0 {
		q += " (" + strings.Join(how, ", ") + ")"
	}
	count := plural(hits, "hit") + " of " + q
	if o.writes {
		count += " that set it"
	}
	then := fmt.Sprintf("then %s in %s", count, plural(len(rest), "file"))
	if hits == 0 {
		then = "no other hits of " + q
		if o.writes {
			then += " that set it"
		}
	}
	if len(o.paths) > 0 || len(o.globs) > 0 {
		then += " (searched " + scopeOf(o, st) + ")"
	}
	fmt.Fprintf(out, "seek: %s%s%s\n", what, sep, then)
	used := 1 + bytes.Count(defsOut.Bytes(), []byte{'\n'})
	if rp.shadowed != "" {
		fmt.Fprintf(out, "(%s is read from the repository root, not as %s)\n", rp.files[0], rp.shadowed)
		used++
	}
	out.Write(defsOut.Bytes())
	if reduced > 0 {
		fmt.Fprintln(out, "(the definition is shortened to fit the budget; raise -b, or -b 0 for no limit)")
		used++
	}
	if len(rest) == 0 {
		return 0
	}
	hitBudget := 100
	if o.bodies {
		hitBudget = 300
	}
	hitBudget-- // the header, as in a search
	if o.budgetSet {
		hitBudget = max(budget-used, 1) // -b bounds the whole output
	}
	ro := render.Options{Budget: hitBudget, Color: color, Context: o.context, Bodies: o.bodies, Counts: o.counts, Path: shown}
	render.Order(rest)
	hl := render.Plan(rest, ro)
	render.Print(out, rest, hl, ro)
	notes(out, hl, rest, ro)
	return 0
}

// readSources reads the files a ref names and lists their definitions as
// targets. A file that isn't searched (binary, too big) is left out; why
// says so when it's the only one.
func readSources(info *repo.Info, files []string) (srcs []source, ts []ref.Target, why string) {
	for _, rel := range files {
		data, skip := search.Read(info.Abs(rel))
		if skip != "" {
			if len(files) == 1 {
				return nil, nil, rel + " is " + skip
			}
			continue
		}
		srcs = append(srcs, source{rel: rel, data: data, at: len(ts)})
		ts = append(ts, ref.Targets(rel, group.Symbols(rel, data))...)
	}
	return srcs, ts, ""
}

// namePattern is what ref mode searches for: the name of the definition
// the ref names, as plain text, with the search options given.
func namePattern(o options, t ref.Target) (string, search.Options, *search.Pattern, error) {
	word := nameWord(t)
	so := o.so
	so.Fixed = true
	p, err := search.Compile(word, so)
	return word, so, p, err
}

// refFiles are the files with hits of the name (-l), in path order; with
// defs, only those that define it (-l -d).
func refFiles(paths []string, p *search.Pattern, ts []ref.Target, m []int, defs bool) ([]*group.File, search.Stats) {
	defined := map[string]bool{}
	for _, i := range m {
		defined[ts[i].Path] = true
	}
	found, st := search.Run(paths, p)
	gs := buildAll(found, p)
	if defs {
		var keep []*group.File
		for _, f := range gs {
			if defined[shown(f.Path)] {
				keep = append(keep, f)
			}
		}
		gs = keep
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].Path < gs[j].Path })
	return gs, st
}

// refDefs are the definitions a ref names, per file, each as a group with
// the hits of its name inside it; their lines (doc comments included), per
// file; and each one's group by its index in ts.
func refDefs(info *repo.Info, srcs []source, ts []ref.Target, m []int, p *search.Pattern) ([]*group.File, map[string][][2]int, map[int]*group.Group) {
	var defFiles []*group.File
	inDefs := map[string][][2]int{}
	whole := map[int]*group.Group{}
	for _, s := range srcs {
		if !slices.ContainsFunc(m, func(i int) bool { return ts[i].Path == s.rel }) {
			continue
		}
		abs := info.Abs(s.rel)
		sf := &search.File{Path: abs, Data: s.data, Lines: search.CountLines(s.data)}
		if found, _ := search.Run([]string{abs}, p); len(found) > 0 {
			sf = found[0]
		}
		sf.Kind = search.Kind(s.rel, s.data) // the repository's folders, not the ones above it
		gf := group.Build(sf, p)
		c := *gf
		c.Groups = nil
		for _, i := range m {
			if ts[i].Path == s.rel {
				g := gf.Whole(i - s.at)
				c.Groups = append(c.Groups, g)
				inDefs[s.rel] = append(inDefs[s.rel], [2]int{g.First(), max(g.End, g.Line)})
				whole[i] = g
			}
		}
		defFiles = append(defFiles, &c)
	}
	return defFiles, inDefs, whole
}

// elsewhere are the hits of the name outside the definitions shown: its
// callers and mentions (with writes, only the hits that set it).
func elsewhere(paths []string, p *search.Pattern, inDefs map[string][][2]int, writes bool) ([]*group.File, search.Stats) {
	found, st := search.Run(paths, p)
	var rest []*group.File
	for _, f := range buildAll(found, p) {
		if f = without(f, inDefs[shown(f.Path)]); f != nil {
			rest = append(rest, f)
		}
	}
	if writes {
		rest = writesOnly(rest)
	}
	return rest, st
}

// without is f without the hits on the given lines (the definitions shown
// above), or nil if none are left.
func without(f *group.File, lines [][2]int) *group.File {
	if len(lines) == 0 {
		return f
	}
	in := func(n int) bool {
		for _, l := range lines {
			if n >= l[0] && n <= l[1] {
				return true
			}
		}
		return false
	}
	c := *f
	c.Groups, c.Defs = nil, nil
	for _, g := range f.Groups {
		var hs []group.Hit
		for _, h := range g.Hits {
			if !in(h.Line) {
				hs = append(hs, h)
			}
		}
		if len(hs) > 0 {
			ng := *g
			ng.Hits = hs
			c.Groups = append(c.Groups, &ng)
		}
	}
	for _, d := range f.Defs {
		if !in(d.Line) {
			c.Defs = append(c.Defs, d)
		}
	}
	if len(c.Groups) == 0 {
		return nil
	}
	return &c
}

// nameWord is what ref mode searches for: the last part of the name (Open
// for Repo.Open, bar for Foo::bar), a heading's whole text, an impl's type.
func nameWord(t ref.Target) string {
	name := t.Name
	switch {
	case len(t.Kind) == 2 && t.Kind[0] == 'h': // a Markdown heading
		return name
	case t.Kind == "impl": // impl Shape for Circle<T>: Circle
		if _, typ, ok := strings.Cut(name, " for "); ok {
			name = typ
		}
		if i := strings.IndexByte(name, '<'); i > 0 {
			name = name[:i]
		}
		name = strings.TrimSpace(name)
	}
	if w := lastPart(name); w != "" {
		return w
	}
	return name
}

// refName is the target part of a symbol's ref: Name, or Name#N.
func refName(r ref.Ref) string {
	r.Path = ""
	return r.String()
}

// sigOf is the signature of a target among the definitions shown.
func sigOf(defFiles []*group.File, t ref.Target) string {
	for _, f := range defFiles {
		if shown(f.Path) != t.Path {
			continue
		}
		for _, g := range f.Groups {
			if g.Line == t.Line && g.Name == t.Name {
				return g.Sig
			}
		}
	}
	return ""
}

// outsideDefs prints a line that no definition contains, with a little
// context: there is no name to search for.
func outsideDefs(out *bufio.Writer, o options, r ref.Ref, srcs []source, color bool) int {
	if len(srcs) == 0 {
		return 1
	}
	s := srcs[0] // a line is in one file (see refSearch)
	loc := r
	loc.Path = s.rel
	if n := search.CountLines(s.data); r.Line > n {
		fmt.Fprintf(out, "seek: %s: %s has %s\n", loc, s.rel, plural(n, "line"))
		return 1
	}
	fmt.Fprintf(out, "seek: %s is outside any definition, so there's no name to search for\n", loc)
	render.Excerpt(out, s.rel, s.data, r.Line, max(o.context, 2), render.Options{Color: color})
	return 1
}

// noDefinition says that a ref names no definition in its files, and lists
// as refs the ones it may have meant: names that contain it, else all of
// them.
func noDefinition(out *bufio.Writer, typed string, r ref.Ref, ts []ref.Target, files []string) int {
	where := strings.Join(files, ", ")
	if len(files) > 3 {
		where = fmt.Sprintf("the %d files whose paths end in %s", len(files), r.Path)
	}
	fmt.Fprintf(out, "seek: %s: no definition named %q in %s\n", typed, refName(r), where)
	part := lastPart(r.Name)
	near, all := candidates(r, ts)
	switch {
	case len(near) > 0:
		fmt.Fprintf(out, "  names that contain %q: %s\n", part, capped(near, 8))
	case len(ts) > 0:
		fmt.Fprintf(out, "  definitions there: %s\n", capped(all, 12))
	case !outline.Supported(files[0]):
		fmt.Fprintln(out, "  seek finds no definitions in this type of file")
	default:
		fmt.Fprintln(out, "  seek finds no definitions there")
	}
	if cmd := command("-F ", typed); cmd != "" {
		fmt.Fprintf(out, "  to search for it as text: %s\n", cmd)
	}
	return 1
}

// candidates are the refs of the definitions a ref that names none may
// have meant: near, whose names contain its name's last part, and all.
func candidates(r ref.Ref, ts []ref.Target) (near, all []string) {
	refs := ref.Refs(ts)
	part := strings.ToLower(lastPart(r.Name))
	for i, t := range ts {
		all = append(all, refs[i].String())
		if strings.Contains(strings.ToLower(t.Name), part) {
			near = append(near, refs[i].String())
		}
	}
	return near, all
}

// capped joins the first n items and says how many more there are.
func capped(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + fmt.Sprintf(", +%d more", len(items)-n)
}
