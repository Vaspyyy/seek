package main

// Context (Prism's docs/context.md): what changed in the working tree and
// on this branch, the commit recap last showed (focus) and crux's last run.
// seek reads it on every run, unless --no-context or PRISM_NO_CONTEXT=1,
// and uses it only to rank and mark: definitions context knows about get
// their marks after their name ("func List [focus]  L73-107") and come
// first, and a context: line on top says what was used. Nothing is ever
// left out because of it.
//
// Ref mode also links the definition to the tests that mention its name,
// and suggests the crux command that runs them. That comes from the result
// itself, not from context, so --no-context keeps it.

import (
	"os"
	"path/filepath"

	"github.com/Vaspyyy/prism/evidence"
	"github.com/Vaspyyy/prism/ref"
	"github.com/Vaspyyy/prism/repo"
	"github.com/Vaspyyy/prism/report"
	"github.com/Vaspyyy/prism/testcmd"
	"github.com/Vaspyyy/prism/work"
	"github.com/Vaspyyy/seek/internal/group"
)

// layers are the parts of the context seek reads (its row in Prism's
// docs/context.md).
const layers = work.Changes | work.Focus | work.LastRun

// background finds the repository seek runs in and then loads the context,
// in a goroutine: that takes a few git commands, which run while seek lists
// and searches the files. Its methods are for the main goroutine only.
type background struct {
	infoCh chan *repo.Info
	cxCh   chan *work.State
	in     *repo.Info
	cx     *work.State
	gotIn  bool
	gotCx  bool
	used   bool // the run asked for the context: its context: line counts
}

// detect starts the background work. With off, the context is off and
// nothing of it is read.
func detect(off bool) *background {
	b := &background{infoCh: make(chan *repo.Info, 1), cxCh: make(chan *work.State, 1)}
	dir, err := os.Getwd()
	go func() {
		var in *repo.Info
		if err == nil {
			in, _ = repo.Detect(dir)
		}
		b.infoCh <- in
		b.cxCh <- work.Load(in, layers, off)
	}()
	return b
}

// info waits for the repository and sets shown.
func (b *background) info() *repo.Info {
	if !b.gotIn {
		b.in, b.gotIn = <-b.infoCh, true
		if b.in != nil {
			shown = printedPaths(b.in)
		}
	}
	return b.in
}

// context waits for the context, for a run that ranks and marks with it.
func (b *background) context() *work.State {
	b.used = true
	return b.wait()
}

// wait waits for the background work to end, also when the run didn't
// need the context: git mustn't still run when seek returns.
func (b *background) wait() *work.State {
	b.info()
	if !b.gotCx {
		b.cx, b.gotCx = <-b.cxCh, true
	}
	return b.cx
}

// line is the context: line for the top of the text report, or "".
func (b *background) line() string {
	if !b.used {
		return ""
	}
	return b.cx.Line()
}

// markFunc is render.Options.Mark: the marks of a printed definition.
func markFunc(cx *work.State) func(string) string {
	if cx == nil || cx.Off {
		return nil
	}
	return func(r string) string { return work.Suffix(cx.Marks(r)) }
}

// groupRef is the ref of the definition containing a group's hits.
func groupRef(path string, g *group.Group) string {
	return ref.Symbol(path, g.Name, g.Ordinal).String()
}

// ranked records that context reordered results, and the marks that did
// it: the context: line names their layers even where the marks aren't
// printed (a summary line, a definition cut by the budget).
func ranked(cx *work.State, why []string) {
	cx.Use(work.Rank)
	cx.Used(why...)
}

// rank puts what context marks first: in each file the groups whose
// definition has marks, and files with such groups before the others,
// stable otherwise. Lock files, generated, minified and vendored code stay
// at the end. It drops nothing and sets each group's Marked. When the
// order changed, it records that and why (ranked).
func rank(gs []*group.File, cx *work.State) {
	if cx == nil || cx.Off {
		return
	}
	var why []string
	fileWhy := map[*group.File][]string{}
	for _, f := range gs {
		path := shown(f.Path)
		for _, g := range f.Groups {
			if g.Sym >= 0 {
				ms := cx.Peek(groupRef(path, g))
				g.Marked = len(ms) > 0
				fileWhy[f] = append(fileWhy[f], ms...)
			}
		}
		if len(fileWhy[f]) > 0 && firstWhere(f.Groups, func(g *group.Group) bool { return g.Marked }) {
			why = append(why, fileWhy[f]...)
		}
	}
	moved := func(f *group.File) bool { return len(fileWhy[f]) > 0 && f.Kind == "" }
	if firstWhere(gs, moved) {
		for _, f := range gs {
			if moved(f) {
				why = append(why, fileWhy[f]...)
			}
		}
	}
	if len(why) > 0 {
		ranked(cx, why)
	}
}

// markedFirst puts the definitions a ref names that context marks first,
// all still shown, and records it when that changed their order.
func markedFirst(m []int, refs []ref.Ref, cx *work.State) {
	if cx == nil || cx.Off {
		return
	}
	marks := map[int][]string{}
	for _, i := range m {
		marks[i] = cx.Peek(refs[i].String())
	}
	if firstWhere(m, func(i int) bool { return len(marks[i]) > 0 }) {
		var why []string
		for _, ms := range marks {
			why = append(why, ms...)
		}
		ranked(cx, why)
	}
}

// anyMarked reports whether a group of the files has marks (after rank).
func anyMarked(gs []*group.File) bool {
	for _, f := range gs {
		for _, g := range f.Groups {
			if g.Marked {
				return true
			}
		}
	}
	return false
}

// firstWhere moves the items that keep reports true to the front, keeping
// the order within both parts, and reports whether anything moved.
func firstWhere[T comparable](s []T, keep func(T) bool) bool {
	var front, back []T
	for _, x := range s {
		if keep(x) {
			front = append(front, x)
		} else {
			back = append(back, x)
		}
	}
	moved := false
	for i, x := range append(front, back...) {
		if s[i] != x {
			moved = true
		}
		s[i] = x
	}
	return moved
}

// contextLine reports whether the text report will likely get a context:
// line, so the budget can make room for it (out of detail lines, never
// out of the files listed): context marks something in the result, or
// says it skipped symbol detail.
func contextLine(cx *work.State, gs ...[]*group.File) bool {
	if cx == nil || cx.Off {
		return false
	}
	if cx.Changes != nil && cx.Changes.Skipped != "" {
		return true
	}
	for _, g := range gs {
		if anyMarked(g) {
			return true
		}
	}
	return false
}

// mention is a test whose hits name the definition a ref shows.
type mention struct {
	test ref.Target
	ref  string // the test's ref
	at   string // the location ref of its first hit there
}

// testMentions finds the tests among the hits of a ref's name elsewhere
// (Prism's testcmd.IsTest: Go, pytest and Rust tests), each once, in the
// order of the hits: a hit counts when it names word as a whole word, in
// the test or in a definition nested in it. It sets the groups' Test. The
// evidence is textual (basis "reference"): the test mentions the name,
// which doesn't say what it tests.
func testMentions(rest []*group.File, word string) []mention {
	var out []mention
	seen := map[string]bool{}
	for _, f := range rest {
		path := shown(f.Path)
		var ts []ref.Target
		var refs []ref.Ref
		for _, g := range f.Groups {
			if g.Sym < 0 || !namesWord(g.Hits, word) {
				continue
			}
			if ts == nil {
				ts = ref.Targets(path, f.Syms)
				refs = ref.Refs(ts)
			}
			for i := g.Sym; i >= 0; i = f.Syms[i].Parent {
				if !testcmd.IsTest(ts[i], f.Data) {
					continue
				}
				r := refs[i]
				g.Test = refName(r)
				if !seen[r.String()] {
					seen[r.String()] = true
					out = append(out, mention{test: ts[i], ref: r.String(), at: ref.Location(path, firstNaming(g.Hits, word), 0).String()})
				}
				break
			}
		}
	}
	return out
}

// namesWord reports whether a hit names word as a whole word, as it's
// written (not in another case, not inside a longer word).
func namesWord(hs []group.Hit, word string) bool {
	return firstNaming(hs, word) > 0
}

// firstNaming is the line of the first hit that names word, or 0.
func firstNaming(hs []group.Hit, word string) int {
	for _, h := range hs {
		for _, s := range h.Spans {
			if h.Text[s[0]:s[1]] == word && wordAt(h.Text, s[0], s[1]) == word {
				return h.Line
			}
		}
	}
	return 0
}

// mentionLinks are the links from each test to the definitions shown:
// "mentions", basis "reference", at its first hit.
func mentionLinks(ms []mention, defs []string) []report.Link {
	var out []report.Link
	for _, m := range ms {
		for _, d := range defs {
			if l, err := evidence.Link(m.ref, d, evidence.Mentions, evidence.Reference, m.at); err == nil {
				out = append(out, l)
			}
		}
	}
	return out
}

// mentionNext suggests the crux commands that run the tests, with the
// reason worded by the evidence: "runs the 1 test that mentions List". At
// most report.MaxNext, the same in the text and the JSON.
func mentionNext(info *repo.Info, ms []mention, word string) []report.Next {
	cwd := workDir()
	if len(ms) == 0 || info == nil || cwd == "" {
		return nil
	}
	var tests []ref.Target
	for _, m := range ms {
		tests = append(tests, m.test)
	}
	next := testcmd.Next(info.Root, cwd, tests, evidence.Mentions, word)
	return next[:min(len(next), report.MaxNext)]
}

// workDir is the working directory with symlinks resolved, as the
// repository's root is, so that paths relative to it come out right.
func workDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(wd); err == nil {
		return r
	}
	return wd
}

// defRefs are the refs of the definitions shown, by their index in ts.
func defRefs(refs []ref.Ref, m []int) []string {
	var out []string
	for _, i := range m {
		out = append(out, refs[i].String())
	}
	return out
}

// symRefOf is the ref of an outline definition -d lists, or "" for one
// the outline rules don't know (a field, a constant), which has no marks.
func symRefOf(path string, d group.Def) string {
	if !d.Outline {
		return ""
	}
	return ref.Symbol(path, d.Name, d.Ordinal).String()
}

// defAt is a definition -d lists, with its file.
type defAt struct {
	f *group.File
	d group.Def
}

// orderedDefs are the definitions -d lists, in order, the ones context
// marks first (recorded when that changed the order).
func orderedDefs(defFiles []*group.File, cx *work.State) []defAt {
	var all []defAt
	for _, f := range defFiles {
		for _, d := range f.Defs {
			all = append(all, defAt{f, d})
		}
	}
	if cx == nil || cx.Off {
		return all
	}
	marks := make([][]string, len(all))
	idx := make([]int, len(all))
	var why []string
	for i, x := range all {
		idx[i] = i
		if r := symRefOf(shown(x.f.Path), x.d); r != "" {
			marks[i] = cx.Peek(r)
			why = append(why, marks[i]...)
		}
	}
	if !firstWhere(idx, func(i int) bool { return len(marks[i]) > 0 }) {
		return all
	}
	ranked(cx, why)
	out := make([]defAt, len(all))
	for k, i := range idx {
		out[k] = all[i]
	}
	return out
}
