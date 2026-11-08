package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Vaspyyy/prism/focus"
	"github.com/Vaspyyy/prism/repo"
	"github.com/Vaspyyy/prism/report"
	"github.com/Vaspyyy/prism/work"
	"github.com/Vaspyyy/seek/internal/group"
	"github.com/Vaspyyy/seek/internal/search"
)

// withContext turns context back on for a test (TestMain turns it off),
// in an environment of its own: no global git config, a fixed author, and
// no agent session, so the focus a test writes is the one seek reads.
func withContext(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"PRISM_NO_CONTEXT": "", "PRISM_SESSION": "", "CLAUDE_CODE_SESSION_ID": "", "T3CODE_THREAD_ID": "",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "T", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "T", "GIT_COMMITTER_EMAIL": "t@example.com",
	} {
		t.Setenv(k, v)
	}
}

// git runs git in the working directory.
func git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The fixture of Prism's v1 acceptance test (docs/roadmap.md): a function
// List with a doc comment, a caller run, and a test TestList that mentions
// List.
const (
	fxMod   = "module example.com/fx\n\ngo 1.26.0\n"
	fxFiles = `package files

// List returns the names under roots, sorted.
// Roots that are files are kept.
func List(roots []string) []string {
	return roots
}
`
	fxTest = `package files

import "testing"

func TestList(t *testing.T) {
	if got := List([]string{"a"}); len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}
`
	fxMain = `package main

import "example.com/fx/internal/files"

func run() {
	_ = files.List(nil)
}

func main() {
	run()
}
`
)

// fixture writes the acceptance fixture as a git repository with one
// commit, and returns the function that writes files.
func fixture(t *testing.T) func(name, text string) {
	t.Helper()
	withContext(t)
	write := project(t)
	write("go.mod", fxMod)
	write("internal/files/files.go", fxFiles)
	write("internal/files/files_test.go", fxTest)
	write("main.go", fxMain)
	git(t, "init", "-q", "-b", "main")
	git(t, "add", "-A")
	git(t, "commit", "-qm", "init")
	return write
}

// setFocus records a focus as recap does after recap show <commit>.
func setFocus(t *testing.T, rev string, symbols ...string) string {
	t.Helper()
	info, err := repo.Detect(".")
	if err != nil {
		t.Fatal(err)
	}
	short := git(t, "rev-parse", "--short", rev)
	f := focus.Focus{Range: short, Old: git(t, "rev-parse", rev+"^"), New: git(t, "rev-parse", rev), Symbols: symbols}
	if err := focus.Open(info).SetFocus("recap", f); err != nil {
		t.Fatal(err)
	}
	return short
}

// marks matches the context marks seek prints after a name.
var marks = regexp.MustCompile(` \[(changed|branch|focus|failing)\]`)

// withoutContext is a text report minus what context adds: the context:
// line and the marks.
func withoutContext(out string) string {
	if strings.HasPrefix(out, "context: ") {
		_, out, _ = strings.Cut(out, "\n")
	}
	return marks.ReplaceAllString(out, "")
}

// sameLines reports whether two reports have the same lines in any order:
// context may reorder, never drop.
func sameLines(a, b string) bool {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	slices.Sort(la)
	slices.Sort(lb)
	return slices.Equal(la, lb)
}

// Step 2 of Prism's v1 acceptance test: after a commit that changes List
// and recap showing it, the seek command recap suggests shows List marked
// [focus], its caller and its test, the test labelled as one that
// mentions List, and the crux command that runs it. Never "covers".
func TestAcceptanceRefMode(t *testing.T) {
	write := fixture(t)
	write("internal/files/files.go", strings.Replace(fxFiles, "return roots", "out := append([]string(nil), roots...)\n\treturn out", 1))
	git(t, "commit", "-qam", "List copies")
	short := setFocus(t, "HEAD", "internal/files/files.go:List")

	out, code := seek(t, "internal/files/files.go:List")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	want := "context: focus: commit " + short + " (recap, just now)\n" +
		`seek: internal/files/files.go:List [focus] (func, L5-8), then 2 hits of "List" in 2 files` + "\n" +
		"internal/files/files.go  8 lines\n" +
		"  func List [focus]  L5-8\n" +
		"    3: // List returns the names under roots, sorted.  (comment)\n" +
		"    4- // Roots that are files are kept.\n" +
		"    5: func List(roots []string) []string {\n" +
		"    6- \tout := append([]string(nil), roots...)\n" +
		"    7- \treturn out\n" +
		"    8- }\n" +
		"main.go  11 lines\n" +
		"  func run  L5-7\n" +
		"    6 _ = files.List(nil)\n" +
		"internal/files/files_test.go  9 lines\n" +
		"  func TestList  L5-9  (test, mentions List)\n" +
		"    6 if got := List([]string{\"a\"}); len(got) != 1 {\n" +
		"next: crux go test -run '^TestList$' ./internal/files/   runs the 1 test that mentions List\n"
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
	reject(t, out, "covers", "cover", "tests List")

	// --no-context and PRISM_NO_CONTEXT=1: the same minus the context: line
	// and the marks; the label and next: line come from the result
	off, _ := seek(t, "--no-context", "internal/files/files.go:List")
	if off != withoutContext(out) {
		t.Errorf("--no-context:\n%s\nwant\n%s", off, withoutContext(out))
	}
	t.Setenv("PRISM_NO_CONTEXT", "1")
	if env, _ := seek(t, "internal/files/files.go:List"); env != off {
		t.Errorf("PRISM_NO_CONTEXT=1:\n%s", env)
	}
	t.Setenv("PRISM_NO_CONTEXT", "")

	// the JSON: marks, the link with its basis, next, context
	d, res, _ := seekJSON(t, "internal/files/files.go:List")
	if len(res.Definitions) != 1 || !slices.Equal(res.Definitions[0].Marks, []string{"focus"}) || res.Definitions[0].Line != 5 {
		t.Errorf("definitions %+v", res.Definitions)
	}
	wantLinks := []report.Link{{From: "internal/files/files_test.go:TestList", To: "internal/files/files.go:List", Rel: "mentions", Basis: "reference", At: "internal/files/files_test.go:6"}}
	if !slices.Equal(d.Links, wantLinks) {
		t.Errorf("links %+v", d.Links)
	}
	wantNext := []report.Next{{Cmd: "crux go test -run '^TestList$' ./internal/files/", Why: "runs the 1 test that mentions List"}}
	if !slices.Equal(d.Next, wantNext) {
		t.Errorf("next %+v", d.Next)
	}
	c := d.Context
	if c == nil || !slices.Equal(c.Applied, []string{"mark"}) || c.Focus == nil || c.Focus.Range != short || c.Focus.By != "recap" || c.Changed == nil || c.Changed.Files != 0 {
		t.Errorf("context %+v", c)
	}
	d, res, _ = seekJSON(t, "--no-context", "internal/files/files.go:List")
	if d.Context != nil || res.Definitions[0].Marks != nil || !slices.Equal(d.Links, wantLinks) || !slices.Equal(d.Next, wantNext) {
		t.Errorf("--no-context: context %+v, marks %v, links %+v, next %+v", d.Context, res.Definitions[0].Marks, d.Links, d.Next)
	}
}

// An uncommitted change marks the definition it's in, wherever seek names
// it, and the context: line says so.
func TestContextMarksUncommitted(t *testing.T) {
	write := fixture(t)
	write("main.go", strings.Replace(fxMain, "_ = files.List(nil)", "_ = files.List([]string{\".\"})", 1))
	out, _ := seek(t, "List")
	expect(t, out, "context: 1 changed symbol vs HEAD\n", "  func run [changed]  L5-7\n", "  func List  L5-7\n")
	if !strings.HasPrefix(out, "context: ") {
		t.Errorf("the context: line comes first:\n%s", out)
	}
	out, _ = seek(t, "-c", "List")
	expect(t, out, "  func run [changed]  L5-7  1 hit")
	out, _ = seek(t, "-d", "run")
	expect(t, out, "context: 1 changed symbol vs HEAD\n", "main.go:run [changed]  L5-7  func run()\n")
	out, _ = seek(t, "main.go:run")
	expect(t, out, "seek: main.go:run [changed] (func, L5-7)", "  func run [changed]  L5-7\n")
	// a name the output doesn't show isn't in the context: line
	out, _ = seek(t, "roots")
	reject(t, out, "context:", "[changed]")

	d, res, _ := seekJSON(t, "List")
	var got []string
	for _, g := range res.Groups {
		if g.Symbol != nil && g.Symbol.Marks != nil {
			got = append(got, g.Symbol.Ref+" "+strings.Join(g.Symbol.Marks, ","))
		}
	}
	wantList(t, "marked", got, []string{"main.go:run changed"})
	if c := d.Context; c == nil || c.Changed == nil || c.Changed.Uncommitted != 1 || c.Changed.Files != 1 || !slices.Contains(c.Applied, "mark") {
		t.Errorf("context %+v", c)
	}
	if _, res, _ := seekJSON(t, "-d", "run"); len(res.Definitions) != 1 || !slices.Equal(res.Definitions[0].Marks, []string{"changed"}) {
		t.Errorf("-d: %+v", res.Definitions)
	}
	// -l prints only paths: no context: line
	out, _ = seek(t, "-l", "List")
	reject(t, out, "context:")
	if d, _, _ := seekJSON(t, "-l", "List"); d.Context == nil {
		t.Error("-l --json: no context")
	}
}

// Marked definitions come first: in a file, files with them before the
// others, and among several definitions of a name. Nothing is dropped,
// and "rank" is recorded only when the order changed.
func TestContextRanks(t *testing.T) {
	withContext(t)
	write := project(t)
	write("lib.go", "package p\n\nfunc helper() {}\n")
	write("a.go", "package p\n\nfunc one() { helper() }\n\nfunc two() { helper() }\n")
	write("b.go", "package p\n\nfunc three() { helper() }\n")
	write("x/open.go", "package x\n\nfunc Open() {}\n")
	write("y/open.go", "package y\n\nfunc Open() {}\n")
	git(t, "init", "-q", "-b", "main")
	git(t, "add", "-A")
	git(t, "commit", "-qm", "init")

	off, _ := seek(t, "helper")
	expect(t, off, "lib.go  3 lines\n  func helper  L3\n    3 func helper() {}\na.go  5 lines\n  func one  L3\n    3 func one() { helper() }\n  func two  L5\n")
	_, res, _ := seekJSON(t, "helper")
	wantList(t, "unchanged", groupsOf(res), []string{"lib.go lib.go:helper: 3", "a.go a.go:one: 3", "a.go a.go:two: 5", "b.go b.go:three: 3"})

	write("a.go", "package p\n\nfunc one() { helper() }\n\nfunc two() { helper(); helper() }\n")
	write("b.go", "package p\n\nfunc three() { helper() }\n\n// four is new.\nfunc four() {}\n")
	out, _ := seek(t, "helper")
	expect(t, out, "context: 2 changed symbols vs HEAD\n", "a.go  5 lines\n  func two [changed]  L5\n    5 func two() { helper(); helper() }\n  func one  L3\n")
	if strings.Index(out, "a.go  ") > strings.Index(out, "lib.go  ") {
		t.Errorf("the file with a changed definition comes first:\n%s", out)
	}
	off, _ = seek(t, "--no-context", "helper")
	if !sameLines(withoutContext(out), off) {
		t.Errorf("context dropped or added lines:\n%s\nwithout:\n%s", out, off)
	}
	d, res, _ := seekJSON(t, "helper")
	wantList(t, "ranked", groupsOf(res), []string{"a.go a.go:two: 5", "a.go a.go:one: 3", "lib.go lib.go:helper: 3", "b.go b.go:three: 3"})
	if !slices.Equal(d.Context.Applied, []string{"rank", "mark"}) {
		t.Errorf("applied %v", d.Context.Applied)
	}
	// the order doesn't change: no "rank"
	if d, _, _ := seekJSON(t, "one"); !slices.Equal(d.Context.Applied, []string{}) {
		t.Errorf("one: applied %v", d.Context.Applied)
	}
	if d, _, _ := seekJSON(t, "two"); !slices.Equal(d.Context.Applied, []string{"mark"}) {
		t.Errorf("two: applied %v", d.Context.Applied)
	}

	// a name with several definitions: the changed one first, both shown
	out, _ = seek(t, "-d", "Open")
	expect(t, out, "x/open.go:Open  L3", "y/open.go:Open  L3")
	reject(t, out, "context:")
	if strings.Index(out, "x/open.go:Open") > strings.Index(out, "y/open.go:Open") {
		t.Errorf("unchanged order:\n%s", out)
	}
	write("y/open.go", "package y\n\nfunc Open() { _ = 1 }\n")
	out, _ = seek(t, "-d", "Open")
	expect(t, out, "context: 3 changed symbols vs HEAD\n", "y/open.go:Open [changed]  L3", "x/open.go:Open  L3")
	if strings.Index(out, "y/open.go:Open") > strings.Index(out, "x/open.go:Open") {
		t.Errorf("the changed definition comes first:\n%s", out)
	}
	if _, res, _ := seekJSON(t, "-d", "Open"); !slices.Equal(refsOf(res.Definitions), []string{"y/open.go:Open", "x/open.go:Open"}) {
		t.Errorf("-d --json: %q", refsOf(res.Definitions))
	}
	out, _ = seek(t, "open.go:Open")
	expect(t, out, "seek: open.go:Open matches 2 definitions: y/open.go:Open [changed] (func, L3), x/open.go:Open (func, L3)")
	if strings.Index(out, "y/open.go  ") > strings.Index(out, "x/open.go  ") {
		t.Errorf("ref mode: the changed definition comes first:\n%s", out)
	}
}

// Ref mode labels the tests among the hits, each once, links them, and
// suggests the command that runs them; a hit that names the definition
// only inside a longer word or in another case doesn't count.
func TestTestMentions(t *testing.T) {
	write := fixture(t)
	write("internal/files/more_test.go", `package files

import "testing"

func TestListAll(t *testing.T) {
	_ = ListAll()
}

func TestTwice(t *testing.T) {
	_ = List(nil)
	_ = List([]string{"x"})
}

func helper() { _ = List(nil) }
`)
	write("lib.py", "def list_files():\n    return []\n")
	write("test_lib.py", `from lib import list_files


def test_list():
    def check():
        assert list_files() == []
    check()


class TestLib:
    def test_more(self):
        assert list_files() == []


def other():
    list_files()
`)
	git(t, "add", "-A")
	git(t, "commit", "-qm", "more")
	out, _ := seek(t, "--partial", "internal/files/files.go:List")
	expect(t, out, "  func TestList  L5-9  (test, mentions List)\n", "  func TestTwice  L9-12  (test, mentions List)\n", "  func helper  L14\n", "  func TestListAll  L5-7\n",
		"next: crux go test -run '^(TestList|TestTwice)$' ./internal/files/   runs the 2 tests that mention List\n")
	reject(t, out, "TestListAll  L5-7  (test")
	d, _, _ := seekJSON(t, "internal/files/files.go:List")
	var from []string
	for _, l := range d.Links {
		from = append(from, l.From+" "+l.At)
	}
	wantList(t, "links", from, []string{"internal/files/files_test.go:TestList internal/files/files_test.go:6", "internal/files/more_test.go:TestTwice internal/files/more_test.go:10"})

	// pytest: a test function, a definition nested in one, a method
	out, _ = seek(t, "lib.py:list_files")
	expect(t, out, "  def test_list.check  L5-6  (in test test_list, mentions list_files)\n", "  def TestLib.test_more  L11-12  (test, mentions list_files)\n", "  def other  L15-16\n",
		"next: crux pytest test_lib.py::TestLib::test_more test_lib.py::test_list   runs the 2 tests that mention list_files\n")
	// from a subdirectory, the command runs as printed there
	if err := os.Chdir("internal"); err != nil {
		t.Fatal(err)
	}
	out, _ = seek(t, "files.go:List")
	expect(t, out, "next: crux go test -run '^(TestList|TestTwice)$' ./files/")
	out, _ = seek(t, "-c", "files.go:List")
	expect(t, out, "  func TestList  L5-9  (test, mentions List)  1 hit: L6\n")
	// squeezed to one line per file, the tests are counted
	out, _ = seek(t, "-b", "14", "files.go:List")
	expect(t, out, "internal/files/more_test.go  14 lines, 3 hits in 2 places (1 test that mentions List): ")
}

// listed is the files a text report lists, in order, and how many more
// its "… N more files" line counts.
func listed(out string) (files []string, more int) {
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "… "):
			if f := strings.Fields(l); len(f) > 1 {
				more, _ = strconv.Atoi(f[1])
			}
		case l == "", l[0] == ' ', strings.HasPrefix(l, "seek: "), strings.HasPrefix(l, "context: "), strings.HasPrefix(l, "next: "), strings.HasPrefix(l, "+"):
		default:
			files = append(files, strings.Fields(l)[0])
		}
	}
	return files, more
}

// Prism's docs/output.md, rule 5: the context: line and next: lines fit in
// the budget only by giving up detail (hit lines, code), never a file that
// would be listed without context; with no detail to give up they go on
// top. Ranking may move files across the cut, but as many are listed.
func TestContextInBudget(t *testing.T) {
	write := fixture(t)
	for i := range 30 {
		write(filepath.Join("pkg", "f"+itoa(i)+".go"), "package pkg\n\nfunc use"+itoa(i)+"() { List(); List() }\n")
	}
	git(t, "add", "-A")
	git(t, "commit", "-qm", "callers")
	check := func(ranks bool, args ...string) {
		t.Helper()
		on, _ := seek(t, args...)
		off, _ := seek(t, append([]string{"--no-context"}, args...)...)
		if !strings.HasPrefix(on, "context: ") {
			t.Errorf("seek %v: no context: line\n%s", args, on)
		}
		fOn, mOn := listed(on)
		fOff, mOff := listed(off)
		if len(fOn) != len(fOff) || mOn != mOff || !ranks && !slices.Equal(fOn, fOff) {
			t.Errorf("seek %v: lists %d files +%d more, %d +%d without context:\n%s\nwithout:\n%s", args, len(fOn), mOn, len(fOff), mOff, on, off)
		}
		// on top only when there's no detail to give up: hit lines
		n, m := strings.Count(on, "\n"), strings.Count(off, "\n")
		detail := regexp.MustCompile(`(?m)^ +\d+ `).MatchString(off)
		if n > m+1 || detail && n > m {
			t.Errorf("seek %v: %d lines, %d without context (detail to give up: %v):\n%s\nwithout:\n%s", args, n, m, detail, on, off)
		}
	}
	budgets := []int{8, 12, 20, 40, 70}
	// the focus marks List, which comes first anyway: no ranking
	setFocus(t, "HEAD", "internal/files/files.go:List")
	for _, b := range budgets {
		check(false, "-b", itoa(b), "internal/files/files.go:List")
		check(false, "-b", itoa(b), "List")
		check(false, "-b", itoa(b), "-s", "List")
	}
	// use3 moves to the front
	setFocus(t, "HEAD", "internal/files/files.go:List", "pkg/f3.go:use3")
	for _, b := range budgets {
		check(true, "-b", itoa(b), "internal/files/files.go:List")
		check(true, "-b", itoa(b), "List")
		check(true, "-b", itoa(b), "-c", "List")
		check(true, "-b", itoa(b), "-s", "List")
		check(false, "-b", itoa(b), "-d", "use3")
	}
}

// Ranking records what moved the results even when no mark gets printed:
// the context: line names the layer, and applied has rank, not mark.
func TestRankAlone(t *testing.T) {
	write := fixture(t)
	write("main.go", strings.Replace(fxMain, "_ = files.List(nil)", "_ = files.List([]string{})", 1))
	git(t, "commit", "-qam", "run")
	short := setFocus(t, "HEAD", "main.go:run")
	info, err := repo.Detect(".")
	if err != nil {
		t.Fatal(err)
	}
	old := shown
	shown = filepath.ToSlash
	t.Cleanup(func() { shown = old })
	file := func(path, name string) *group.File {
		return &group.File{File: &search.File{Path: path}, Groups: []*group.Group{{Sym: 0, Kind: "func", Name: name}}}
	}
	gs := []*group.File{file("internal/files/files.go", "List"), file("main.go", "run")}
	cx := work.Load(info, layers, false)
	rank(gs, cx)
	if gs[0].Path != "main.go" || !gs[0].Groups[0].Marked {
		t.Errorf("not ranked: %s first", gs[0].Path)
	}
	if got, want := cx.Line(), "context: focus: commit "+short+" (recap, just now)"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	if a := cx.Applied(); !slices.Equal(a, []string{"rank"}) {
		t.Errorf("applied %v", a)
	}
	// nothing moved: nothing recorded
	cx = work.Load(info, layers, false)
	rank(gs, cx)
	if cx.Line() != "" || len(cx.Applied()) != 0 {
		t.Errorf("already in order: %q %v", cx.Line(), cx.Applied())
	}
}
