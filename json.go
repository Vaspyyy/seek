package main

// --json prints one JSON document per run instead of the text: Prism's
// envelope (docs/output.md) with a report.Seek as its result, also on
// failure. The shape is defined in prism/report, shared with glint, recap
// and crux.
//
// What goes in it:
//   - groups: the hits per file and containing definition, in the order the
//     text lists them; symbol is null for hits outside any definition, and
//     file_kind is the kind the text shows ("generated", "vendored", …).
//   - a hit's text is its line without leading and trailing whitespace (cut
//     around the first match when it's longer than maxText bytes); col is
//     the first match's 1-based byte column in the file, like compilers.
//   - label is seek's label ("definition", "declaration", "assignment",
//     "import", "comment", "string"), left out for a plain use; writes is
//     set for the hits --writes keeps.
//   - query says how the pattern was read, skipped what wasn't searched
//     (the text's "not searched" notes, always, not only without hits), and
//     counts.searched how many files were.
//   - -s, -c and -C change only the text's layout: the JSON lists every hit
//     either way, and a definition's lines are in its symbol.
//   - the text's line budget doesn't apply: every hit is listed, up to
//     maxItems per list. An explicit -b N lists at most N hits (with -d, N
//     definitions). truncated says when a list was cut.
//   - context (see context.go) orders the groups and definitions as in the
//     text, puts its marks in each symbol's marks, and is summed up in
//     context (null with --no-context).
//   - in ref mode, links has one "mentions" link (basis "reference") from
//     each test whose hits name the definition to the definition, and next
//     the crux commands that run those tests, as the text's next: lines.

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Vaspyyy/prism/ref"
	"github.com/Vaspyyy/prism/repo"
	"github.com/Vaspyyy/prism/report"
	"github.com/Vaspyyy/prism/work"
	"github.com/Vaspyyy/seek/internal/group"
	"github.com/Vaspyyy/seek/internal/render"
	"github.com/Vaspyyy/seek/internal/search"
)

// maxItems caps each list in the JSON (a variable so tests can lower it).
var maxItems = report.MaxItems

// maxText is the longest hit text the JSON carries, in bytes; longer lines
// (minified code) are cut around the first match.
const maxText = 1000

// jsonFlag: --json is among the flags, also after one parse rejected
// (parse stops at the first bad one). It skips the values of -e, -C, -b
// and -g, and stops at --.
func jsonFlag(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return false
		case a == "--json":
			return true
		case a == "-e" || a == "-C" || a == "-b" || a == "-g":
			i++
		case len(a) > 2 && a[0] == '-' && a[1] != '-':
			// combined short flags: the first of C, b, g, e takes the rest, or
			// the next argument when it's last (-ie PATTERN)
			if k := strings.IndexAny(a[1:], "Cbge"); k >= 0 && k+2 == len(a) {
				i++
			}
		}
	}
	return false
}

// envelope starts the JSON document. Outside git, repo.root is the working
// directory; it is set even when the repository couldn't be read.
func envelope(args []string, info *repo.Info) *report.Envelope {
	env := report.New("seek", version, args, info)
	if env.Repo.Root == "" {
		if wd, err := os.Getwd(); err == nil {
			env.Repo.Root = wd
		}
	}
	return env
}

// failJSON prints an envelope with an error instead of a result and
// returns exit. cx is nil when the context isn't known yet.
func failJSON(out io.Writer, args []string, info *repo.Info, cx *work.State, code, msg string, cands []string, exit int) int {
	env := envelope(args, info)
	env.Fail(code, msg, capItems(cands, maxItems)...)
	env.Context = cx.Context()
	report.Write(out, env)
	return exit
}

// newSeek is an empty result for a search with p: the lists that can be
// empty are [], not null.
func newSeek(pattern string, p *search.Pattern, so search.Options) report.Seek {
	return report.Seek{Pattern: pattern, Definitions: []report.Symbol{}, Groups: []report.SeekGroup{},
		Query: report.SeekQuery{WholeWord: p.Smart || so.Words, IgnoreCase: p.Fold, PlainText: p.Text}}
}

// searched fills in what a search looked at: the files searched, and the
// notes on what it didn't search.
func searched(res *report.Seek, o options, st search.Stats) {
	res.Counts.Searched = st.Files
	res.Skipped = notSearched(o, st, true)
	if st.TooBig > 0 {
		res.Skipped = append(res.Skipped, fmt.Sprintf("%s over %d MB", plural(st.TooBig, "file"), search.MaxSize>>20))
	}
}

// searchJSON prints a search's result (seek PATTERN, -d, -l, and -s or -c,
// which list the same).
func searchJSON(out io.Writer, args []string, o options, p *search.Pattern, gs []*group.File, allHits int, paths []string, st search.Stats, bg *background) int {
	env := envelope(args, bg.info())
	cx := bg.context()
	res := newSeek(o.pattern, p, o.so)
	hits := countHits(gs)
	res.Counts = report.SeekCounts{Hits: hits, Files: len(gs), Definitions: countDefs(gs)}
	searched(&res, o, st)
	found := len(gs) > 0
	switch {
	case o.list:
		res.Files, env.Truncated = jsonFiles(gs)
	case o.defs:
		defFiles, total, _ := keptDefs(o, p, gs)
		ordered := orderedDefs(defFiles, cx)
		var defs []report.Symbol
		for _, x := range ordered {
			defs = append(defs, defSymbol(shown(x.f.Path), x.d, cx))
		}
		res.Definitions, env.Truncated = capList(defs, limit(o))
		res.Counts.Definitions = total
		found = total > 0
	default:
		render.Order(gs)
		rank(gs, cx)
		res.Groups, env.Truncated = jsonGroups(gs, limit(o), cx)
		found = hits > 0
		if o.writes {
			if n := allHits - hits; n > 0 {
				if cmd := command("", o.pattern); cmd != "" {
					env.Next = append(env.Next, report.Next{Cmd: cmd, Why: fmt.Sprintf("+%s that don't set it", plural(n, "more hit"))})
				}
			}
		} else if hits > 0 {
			if m := missed(o, p, paths, hits); m.lines > 0 {
				if cmd := command(m.flags, o.pattern); cmd != "" {
					env.Next = append(env.Next, report.Next{Cmd: cmd, Why: m.why()})
				}
			}
		}
	}
	env.Result = res
	env.Context = cx.Context()
	report.Write(out, env)
	return exitCode(found)
}

// refJSON prints what a ref names: the definitions it resolved to, and the
// hits of the name elsewhere (callers and mentions), as refSearch shows
// them. A ref that names nothing is an error with the refs it may have
// meant as candidates.
func refJSON(out io.Writer, args []string, o options, info *repo.Info, rp refPattern, paths []string, bg *background) int {
	r := rp.r
	typed := strings.TrimSpace(o.pattern)
	fail := func(code, msg string, cands []string, exit int) int {
		return failJSON(out, args, info, bg.wait(), code, msg, cands, exit)
	}
	if r.Rev != "" {
		at := r
		at.Rev = ""
		if len(rp.files) == 1 {
			at.Path = rp.files[0]
		}
		return fail(report.CodeUsage, fmt.Sprintf("%s names commit %s, but seek searches the working tree; for that version use: recap show %s --show %s",
			typed, r.Rev, shellQuote(r.Rev), shellQuote(at.String())), nil, 2)
	}
	if r.IsLocation() && len(rp.files) > 1 {
		var locs []string
		for _, f := range rp.files {
			loc := r
			loc.Path = f
			locs = append(locs, loc.String())
		}
		return fail(report.CodeAmbiguous, fmt.Sprintf("%s: %s end in %s; name one", typed, plural(len(rp.files), "path"), r.Path), locs, 1)
	}
	srcs, ts, why := readSources(info, rp.files)
	if why != "" {
		return fail(report.CodeFailed, why, nil, 2)
	}
	m := ref.Match(r, ts, rp.files)
	if len(m) == 0 && r.IsLocation() {
		if len(srcs) == 0 {
			return fail(report.CodeNoMatch, typed+": the file isn't searched", nil, 1)
		}
		loc := r
		loc.Path = srcs[0].rel
		if n := search.CountLines(srcs[0].data); r.Line > n {
			return fail(report.CodeNoMatch, fmt.Sprintf("%s: %s has %s", loc, srcs[0].rel, plural(n, "line")), nil, 1)
		}
		return fail(report.CodeNoMatch, loc.String()+" is outside any definition, so there's no name to search for", nil, 1)
	}
	if len(m) == 0 {
		where := strings.Join(rp.files, ", ")
		if len(rp.files) > 3 {
			where = fmt.Sprintf("the %d files whose paths end in %s", len(rp.files), r.Path)
		}
		msg := fmt.Sprintf("%s: no definition named %q in %s", typed, refName(r), where)
		near, all := candidates(r, ts)
		if len(near) == 0 {
			near = all
		}
		if len(ts) == 0 {
			msg += "; seek finds no definitions there"
		}
		return fail(report.CodeNoMatch, msg, near, 1)
	}
	word, so, p, err := namePattern(o, ts[m[0]])
	if err != nil {
		return fail(report.CodeFailed, err.Error(), nil, 2)
	}

	env := envelope(args, info)
	res := newSeek(word, p, so)
	res.Target = refTarget(r, rp)
	res.Counts.Definitions = len(m)
	found := true
	var cx *work.State
	if o.list {
		gs, st := refFiles(paths, p, ts, m, o.defs)
		cx = bg.context()
		res.Files, env.Truncated = jsonFiles(gs)
		res.Counts.Hits, res.Counts.Files = countHits(gs), len(gs)
		searched(&res, o, st)
		found = len(gs) > 0
	} else {
		_, inDefs, whole := refDefs(info, srcs, ts, m, p)
		var rest []*group.File
		var st search.Stats
		if !o.defs { // -d: the definitions only, as the text prints them
			rest, st = elsewhere(paths, p, inDefs, o.writes)
		}
		cx = bg.context()
		refs := ref.Refs(ts)
		markedFirst(m, refs, cx)
		var defs []report.Symbol
		for _, i := range m {
			if g := whole[i]; g != nil {
				defs = append(defs, *groupSymbol(ts[i].Path, g, cx))
			}
		}
		var cut *report.Truncated
		res.Definitions, cut = capList(defs, maxItems)
		if !o.defs {
			render.Order(rest)
			rank(rest, cx)
			ms := testMentions(rest, word)
			env.Links = append(env.Links, mentionLinks(ms, defRefs(refs, m))...)
			env.Next = append(env.Next, mentionNext(info, ms, word)...)
			res.Groups, env.Truncated = jsonGroups(rest, limit(o), cx)
			res.Counts.Hits, res.Counts.Files = countHits(rest), len(rest)
			searched(&res, o, st)
		}
		if env.Truncated == nil {
			env.Truncated = cut
		}
	}
	env.Result = res
	env.Context = cx.Context()
	report.Write(out, env)
	return exitCode(found)
}

// refTarget is the ref given, with its path from the repository root when
// it names one file ("files.go:List" is "internal/files/files.go:List"),
// so that any tool takes it from anywhere in the repository.
func refTarget(r ref.Ref, rp refPattern) string {
	if len(rp.files) == 1 {
		r.Path = rp.files[0]
	}
	return r.String()
}

// limit is how many hits (with -d, definitions) the JSON lists: all, up to
// maxItems, unless -b N is given.
func limit(o options) int {
	if o.budgetSet && o.budget > 0 {
		return min(o.budget, maxItems)
	}
	return maxItems
}

// jsonGroups are the files' groups with at most n hits in all, and what
// was cut.
func jsonGroups(gs []*group.File, n int, cx *work.State) ([]report.SeekGroup, *report.Truncated) {
	out := []report.SeekGroup{}
	listed, total := 0, countHits(gs)
	for _, f := range gs {
		path := shown(f.Path)
		for _, g := range f.Groups {
			if listed >= n {
				break
			}
			hs := g.Hits[:min(len(g.Hits), n-listed)]
			sg := report.SeekGroup{Path: path, FileKind: f.Kind, Symbol: groupSymbol(path, g, cx), Hits: make([]report.SeekHit, len(hs))}
			for i, h := range hs {
				sg.Hits[i] = jsonHit(h)
			}
			listed += len(hs)
			out = append(out, sg)
		}
	}
	return out, truncated(listed, total)
}

// jsonFiles are the paths of -l, from the repository root (the text prints
// them from the working directory, for the commands it's piped to). It is
// never nil: with -l, "files" is there, also when it's empty.
func jsonFiles(gs []*group.File) (*[]string, *report.Truncated) {
	files := make([]string, 0, len(gs))
	for _, f := range gs {
		files = append(files, shown(f.Path))
	}
	sort.Strings(files)
	files, cut := capList(files, maxItems)
	return &files, cut
}

func jsonHit(h group.Hit) report.SeekHit {
	sh := report.SeekHit{Line: h.Line, Text: hitText(h), Label: h.Label, Writes: setsName(h)}
	if len(h.Spans) > 0 {
		sh.Col = h.Spans[0][0] + 1
	}
	return sh
}

// hitText is a hit's line without its indentation and trailing space, cut
// around the first match when it's longer than maxText bytes.
func hitText(h group.Hit) string {
	text := strings.TrimRight(h.Text, " \t\r")
	lead := len(text) - len(strings.TrimLeft(text, " \t"))
	text = text[lead:]
	if len(text) <= maxText {
		return text
	}
	at := 0
	if len(h.Spans) > 0 {
		at = max(h.Spans[0][0]-lead, 0)
	}
	from := max(0, at-maxText/4)
	for from > 0 && !utf8.RuneStart(text[from]) {
		from--
	}
	to := min(len(text), from+maxText)
	for to < len(text) && !utf8.RuneStart(text[to]) {
		to++
	}
	s := text[from:to]
	if from > 0 {
		s = "…" + s
	}
	if to < len(text) {
		s += "…"
	}
	return s
}

// groupSymbol is the definition containing a group's hits, with its
// context marks, or nil for the hits outside any.
func groupSymbol(path string, g *group.Group, cx *work.State) *report.Symbol {
	if g.Sym < 0 {
		return nil
	}
	r := groupRef(path, g)
	return &report.Symbol{Ref: r, Path: path, Name: g.Name, Kind: g.Kind,
		Line: g.Line, End: g.End, Doc: docLines(g.Doc, g.Line), Sig: g.Sig, Marks: cx.Marks(r)}
}

// defSymbol is a definition -d lists. One the outline rules don't know (a
// field, a constant, a definition by keyword) has its line as its ref, as
// the text prints it: tools resolve it to the definition containing it.
func defSymbol(path string, d group.Def, cx *work.State) report.Symbol {
	s := report.Symbol{Path: path, Name: d.Name, Kind: d.Kind, Line: d.Line, End: d.End, Sig: d.Sig}
	if d.Outline {
		s.Ref = symRefOf(path, d)
		s.Doc = docLines(d.Doc, d.Line)
		s.Marks = cx.Marks(s.Ref)
	} else {
		s.Ref = ref.Location(path, d.Line, 0).String()
	}
	return s
}

// docLines are the first and last line of a doc comment that starts at doc
// above a definition on line, or nil if there is none.
func docLines(doc, line int) []int {
	if doc > 0 && doc < line {
		return []int{doc, line - 1}
	}
	return nil
}

func countDefs(gs []*group.File) int {
	n := 0
	for _, f := range gs {
		n += len(f.Defs)
	}
	return n
}

// capList keeps at most n items, and says what was cut. The result is
// never nil, so an empty list is printed as [].
func capList[T any](items []T, n int) ([]T, *report.Truncated) {
	total := len(items)
	if items == nil {
		items = []T{}
	}
	items = capItems(items, n)
	return items, truncated(len(items), total)
}

func capItems[T any](items []T, n int) []T {
	if len(items) > n {
		return items[:n]
	}
	return items
}

func truncated(listed, total int) *report.Truncated {
	if listed < total {
		return &report.Truncated{Shown: listed, Total: total}
	}
	return nil
}
