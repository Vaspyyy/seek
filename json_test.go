package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Vaspyyy/prism/ref"
	"github.com/Vaspyyy/prism/report"
)

// jsonRun runs seek with args as given and checks what every --json run
// must print: one envelope on stdout and nothing on stderr, with every ref
// in it naming a file in the repository (except those in loose, refs whose
// path is the end of one). It returns the envelope, its result and the
// exit code.
func jsonRun(t *testing.T, loose []string, args ...string) (*report.Decoded, report.Seek, int) {
	t.Helper()
	var b, e bytes.Buffer
	w := bufio.NewWriter(&b)
	code := run(args, w, &e, false)
	w.Flush()
	if e.Len() > 0 {
		t.Errorf("seek %v wrote to stderr: %s", args, e.String())
	}
	d, err := report.Check(b.Bytes())
	if err != nil {
		t.Fatalf("seek %v: %v\n%s", args, err, b.String())
	}
	if d.Tool != "seek" || d.Version != version || !slices.Equal(d.Args, args) {
		t.Errorf("seek %v: tool %q, version %q, args %q", args, d.Tool, d.Version, d.Args)
	}
	refs, err := report.Refs(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range refs {
		p := ref.Parse(r, nil).Path
		if _, err := os.Stat(filepath.Join(d.Repo.Root, filepath.FromSlash(p))); err != nil && !slices.Contains(loose, r) {
			t.Errorf("seek %v: ref %q doesn't name a file: %v", args, r, err)
		}
	}
	var res report.Seek
	if d.Error == nil {
		if err := json.Unmarshal(d.Result, &res); err != nil {
			t.Fatalf("seek %v: result: %v", args, err)
		}
	}
	return d, res, code
}

// seekJSON runs seek --json args.
func seekJSON(t *testing.T, args ...string) (*report.Decoded, report.Seek, int) {
	t.Helper()
	return jsonRun(t, nil, append([]string{"--json"}, args...)...)
}

// groupsOf lists a result's groups as "path ref: line line …" (ref "-" for
// hits outside any definition), to compare in one go.
func groupsOf(res report.Seek) []string {
	var out []string
	for _, g := range res.Groups {
		s := g.Path + " -"
		if g.Symbol != nil {
			s = g.Path + " " + g.Symbol.Ref
		}
		s += ":"
		for _, h := range g.Hits {
			s += " " + itoa(h.Line)
		}
		out = append(out, s)
	}
	return out
}

func refsOf(syms []report.Symbol) []string {
	var out []string
	for _, s := range syms {
		out = append(out, s.Ref)
	}
	return out
}

func wantList(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

func hitAt(t *testing.T, res report.Seek, path string, line int) report.SeekHit {
	t.Helper()
	for _, g := range res.Groups {
		for _, h := range g.Hits {
			if g.Path == path && h.Line == line {
				return h
			}
		}
	}
	t.Fatalf("no hit at %s:%d in %q", path, line, groupsOf(res))
	return report.SeekHit{}
}

// The groups are the text's: per file and containing definition, with refs
// that carry "#N" where a name is defined twice, and a null symbol for
// lines outside any definition.
func TestJSONSearch(t *testing.T) {
	refProject(t)
	d, res, code := seekJSON(t, "parse")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	wd, _ := os.Getwd()
	if root, _ := filepath.EvalSymlinks(wd); d.Repo.Root != root || !d.Repo.Git || d.Repo.GitDir == "" {
		t.Errorf("repo: %+v, want root %s", d.Repo, root)
	}
	if d.Truncated != nil || len(d.Links) != 0 {
		t.Errorf("truncated %v, links %v", d.Truncated, d.Links)
	}
	if res.Pattern != "parse" || res.Target != "" || len(res.Definitions) != 0 || res.Files != nil {
		t.Errorf("result: %+v", res)
	}
	wantList(t, "groups", groupsOf(res), []string{"src/lib.cpp src/lib.cpp:parse#1: 1", "src/lib.cpp src/lib.cpp:parse#2: 5 6", "src/lib.cpp src/lib.cpp:use: 10"})
	if res.Counts != (report.SeekCounts{Hits: 4, Files: 1, Definitions: 2}) {
		t.Errorf("counts %+v", res.Counts)
	}
	s := res.Groups[1].Symbol
	if s.Path != "src/lib.cpp" || s.Name != "parse" || s.Kind != "func" || s.Line != 5 || s.End != 7 || s.Sig != "int parse(int n)" || s.Doc != nil {
		t.Errorf("symbol %+v", s)
	}
	want := map[int]report.SeekHit{
		1:  {Line: 1, Col: 5, Text: "int parse(const char* s) {", Label: "definition", Writes: true},
		6:  {Line: 6, Col: 12, Text: `return parse("x") + n;`},
		10: {Line: 10, Col: 5, Text: "parse(2);"},
	}
	for line, w := range want {
		if h := hitAt(t, res, "src/lib.cpp", line); h != w {
			t.Errorf("line %d: %+v, want %+v", line, h, w)
		}
	}

	// a doc comment's lines, a hit in it, a file without definitions
	_, res, _ = seekJSON(t, "List")
	wantList(t, "groups", groupsOf(res), []string{
		"internal/files/files.go internal/files/files.go:List: 5 7",
		"main.go main.go:run: 6 7",
		"notes.txt -: 2",
	})
	if s := res.Groups[0].Symbol; !slices.Equal(s.Doc, []int{5, 6}) || s.Line != 7 || s.End != 9 || s.Sig != "func List(roots []string) []string" {
		t.Errorf("symbol %+v", s)
	}
	if h := hitAt(t, res, "internal/files/files.go", 5); h.Label != "comment" || h.Text != "// List returns the names." || h.Writes {
		t.Errorf("doc comment hit %+v", h)
	}
	if h := hitAt(t, res, "notes.txt", 2); h.Text != "see internal/files/files.go:List" || h.Col != 29 {
		t.Errorf("notes hit %+v", h)
	}

	// top level in a file with definitions: the import's path
	_, res, _ = seekJSON(t, "files", "main.go")
	wantList(t, "groups", groupsOf(res), []string{"main.go -: 3", "main.go main.go:run: 6"})
	if h := hitAt(t, res, "main.go", 3); h.Label != "string" || h.Col != 20 {
		t.Errorf("import hit %+v", h)
	}

	// no hits: a result, not an error, and exit 1
	d, res, code = seekJSON(t, "zzqq")
	if code != 1 || d.Error != nil || res.Groups == nil || len(res.Groups) != 0 || res.Counts != (report.SeekCounts{}) {
		t.Errorf("no hits: exit %d, %+v, %+v", code, d.Error, res)
	}
}

// -s, -c and -C change the text's layout only; -d lists definitions, -l
// paths (from the repository root, also in a subdirectory).
func TestJSONModes(t *testing.T) {
	refProject(t)
	_, plain, _ := seekJSON(t, "parse")
	for _, flag := range []string{"-s", "-c", "-C2", "-A"} {
		if _, res, code := seekJSON(t, flag, "parse"); code != 0 || !reflect.DeepEqual(res, plain) {
			t.Errorf("%s: exit %d\n%+v\nwant %+v", flag, code, res, plain)
		}
	}

	_, res, code := seekJSON(t, "-d", "parse")
	if code != 0 || len(res.Groups) != 0 {
		t.Errorf("-d: exit %d, groups %q", code, groupsOf(res))
	}
	wantList(t, "-d", refsOf(res.Definitions), []string{"src/lib.cpp:parse#1", "src/lib.cpp:parse#2"})
	if s := res.Definitions[0]; s.Sig != "int parse(const char* s)" || s.Line != 1 || s.End != 3 || s.Name != "parse" {
		t.Errorf("-d: %+v", s)
	}
	if res.Counts != (report.SeekCounts{Hits: 4, Files: 1, Definitions: 2}) {
		t.Errorf("-d counts %+v", res.Counts)
	}
	_, res, _ = seekJSON(t, "-d", "-s", "List")
	wantList(t, "-d -s", refsOf(res.Definitions), []string{"internal/files/files.go:List"})
	if s := res.Definitions[0]; !slices.Equal(s.Doc, []int{5, 6}) {
		t.Errorf("-d doc %v", s.Doc)
	}
	_, res, code = seekJSON(t, "-d", "zzqq")
	if code != 1 || res.Definitions == nil || len(res.Definitions) != 0 {
		t.Errorf("-d, none: exit %d, %+v", code, res)
	}

	files := []string{"internal/files/files.go", "main.go", "notes.txt"}
	_, res, code = seekJSON(t, "-l", "List")
	if code != 0 || len(res.Groups) != 0 {
		t.Errorf("-l: exit %d, %+v", code, res)
	}
	wantList(t, "-l", res.Files, files)
	if res.Counts.Files != 3 || res.Counts.Hits != 5 {
		t.Errorf("-l counts %+v", res.Counts)
	}
	_, res, _ = seekJSON(t, "-l", "-d", "List")
	wantList(t, "-l -d", res.Files, []string{"internal/files/files.go"})
	if err := os.Chdir("internal"); err != nil {
		t.Fatal(err)
	}
	_, res, _ = seekJSON(t, "-l", "List")
	wantList(t, "-l in a subdirectory", res.Files, files[:1])
	_, res, _ = seekJSON(t, "List", "files")
	wantList(t, "a path in a subdirectory", groupsOf(res), []string{"internal/files/files.go internal/files/files.go:List: 5 7"})
}

// Outside git, paths are relative to the working directory, which is the
// root. A field is a definition whose ref is its line; writes marks the
// hits that set the name.
func TestJSONOutsideGit(t *testing.T) {
	write := project(t)
	write("shop/repo.go", goSrc)
	d, res, _ := seekJSON(t, "-d", "carts")
	wd, _ := os.Getwd()
	root, _ := filepath.EvalSymlinks(wd)
	if d.Repo.Git || d.Repo.Root != root || d.Repo.Head != "" {
		t.Errorf("repo %+v, want %s outside git", d.Repo, root)
	}
	wantList(t, "-d", refsOf(res.Definitions), []string{"shop/repo.go:7"})
	if s := res.Definitions[0]; s.Name != "Repo.carts" || s.Kind != "field" || s.Line != 7 || s.Sig != "carts map[string]*Cart" {
		t.Errorf("field %+v", s)
	}

	_, res, _ = seekJSON(t, "Open")
	wantList(t, "groups", groupsOf(res), []string{"shop/repo.go shop/repo.go:Repo.Open: 10 11 14 15", "shop/repo.go shop/repo.go:use: 22"})
	for line, writes := range map[int]bool{10: false, 11: true, 14: false, 15: false, 22: false} {
		if h := hitAt(t, res, "shop/repo.go", line); h.Writes != writes {
			t.Errorf("line %d: writes %v", line, h.Writes)
		}
	}
	if s := res.Groups[0].Symbol; !slices.Equal(s.Doc, []int{10, 10}) || s.Name != "Repo.Open" {
		t.Errorf("method %+v", s)
	}
	if h := hitAt(t, res, "shop/repo.go", 15); h.Label != "string" || h.Text != `msg := "Open failed"` {
		t.Errorf("string hit %+v", h)
	}

	_, res, _ = seekJSON(t, "--writes", "total")
	wantList(t, "--writes", groupsOf(res), []string{"shop/repo.go shop/repo.go:Repo.Open: 12 13"})
	if h := hitAt(t, res, "shop/repo.go", 12); h.Label != "declaration" || !h.Writes {
		t.Errorf("declaration %+v", h)
	}
	if h := hitAt(t, res, "shop/repo.go", 13); h.Label != "assignment" || !h.Writes {
		t.Errorf("assignment %+v", h)
	}
	d, res, code := seekJSON(t, "--writes", "strings")
	if code != 1 || len(res.Groups) != 0 || len(d.Next) != 1 || d.Next[0].Cmd != "seek strings" || d.Next[0].Why != "+2 more hits that don't set it" {
		t.Errorf("--writes, none: exit %d, %+v, next %+v", code, res, d.Next)
	}

	if err := os.Chdir("shop"); err != nil {
		t.Fatal(err)
	}
	d, res, _ = seekJSON(t, "Open")
	if d.Repo.Root != filepath.Join(root, "shop") || res.Groups[0].Path != "repo.go" || res.Groups[0].Symbol.Ref != "repo.go:Repo.Open" {
		t.Errorf("in a subdirectory: root %s, %q", d.Repo.Root, groupsOf(res))
	}
}

// What the text prints after the header goes to next when it suggests a
// command: here the hits in another case or inside longer words.
func TestJSONNext(t *testing.T) {
	write := project(t)
	write("web/controls.js", jsSrc)
	d, _, _ := seekJSON(t, "speed")
	if len(d.Next) != 1 || d.Next[0].Cmd != "seek -i --partial speed" || !strings.HasPrefix(d.Next[0].Why, "+") || !strings.Contains(d.Next[0].Why, "setSpeed") {
		t.Errorf("next %+v", d.Next)
	}
}

// A ref: target is the ref given, its path from the repository root;
// definitions what it names; groups the hits of the name elsewhere.
func TestJSONRef(t *testing.T) {
	write := refProject(t)
	_, res, code := seekJSON(t, "files.go:List")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if res.Target != "internal/files/files.go:List" || res.Pattern != "List" {
		t.Errorf("target %q, pattern %q", res.Target, res.Pattern)
	}
	wantList(t, "definitions", refsOf(res.Definitions), []string{"internal/files/files.go:List"})
	if s := res.Definitions[0]; !slices.Equal(s.Doc, []int{5, 6}) || s.Line != 7 || s.End != 9 || s.Sig != "func List(roots []string) []string" || s.Kind != "func" {
		t.Errorf("definition %+v", s)
	}
	wantList(t, "groups", groupsOf(res), []string{"main.go main.go:run: 6 7", "notes.txt -: 2"})
	if res.Counts != (report.SeekCounts{Hits: 3, Files: 2, Definitions: 1}) {
		t.Errorf("counts %+v", res.Counts)
	}

	_, res, _ = seekJSON(t, "lib.cpp:parse#2")
	if res.Target != "src/lib.cpp:parse#2" {
		t.Errorf("target %q", res.Target)
	}
	wantList(t, "#2", refsOf(res.Definitions), []string{"src/lib.cpp:parse#2"})
	wantList(t, "#2 groups", groupsOf(res), []string{"src/lib.cpp src/lib.cpp:parse#1: 1", "src/lib.cpp src/lib.cpp:use: 10"})
	_, res, _ = seekJSON(t, "src/lib.cpp:parse")
	wantList(t, "both", refsOf(res.Definitions), []string{"src/lib.cpp:parse#1", "src/lib.cpp:parse#2"})
	wantList(t, "both groups", groupsOf(res), []string{"src/lib.cpp src/lib.cpp:use: 10"})

	_, res, code = seekJSON(t, "-d", "files.go:List")
	if code != 0 || len(res.Groups) != 0 || res.Counts != (report.SeekCounts{Definitions: 1}) {
		t.Errorf("-d: exit %d, %+v", code, res)
	}
	wantList(t, "-d", refsOf(res.Definitions), []string{"internal/files/files.go:List"})
	_, res, _ = seekJSON(t, "-l", "files.go:List")
	wantList(t, "-l", res.Files, []string{"internal/files/files.go", "main.go", "notes.txt"})
	_, res, _ = seekJSON(t, "-l", "-d", "files.go:List")
	wantList(t, "-l -d", res.Files, []string{"internal/files/files.go"})

	for _, r := range []string{"main.go:6", "main.go:6:7", "main.go:6-11"} {
		_, res, code = seekJSON(t, r)
		if code != 0 || res.Target != r {
			t.Errorf("%s: exit %d, target %q", r, code, res.Target)
		}
		wantList(t, r, refsOf(res.Definitions), []string{"main.go:run"})
		wantList(t, r+" groups", groupsOf(res), []string{"main.go main.go:main: 11", "notes.txt -: 1"})
	}

	// the end of a path that fits two files: the target stays as given
	write("a/util.go", "package a\n\nfunc helper() {}\n")
	write("b/util.go", "package b\n\nfunc helper() {}\n\nfunc other() { helper() }\n")
	_, res, _ = jsonRun(t, []string{"util.go:helper"}, "--json", "util.go:helper")
	if res.Target != "util.go:helper" {
		t.Errorf("target %q", res.Target)
	}
	wantList(t, "two files", refsOf(res.Definitions), []string{"a/util.go:helper", "b/util.go:helper"})

	if err := os.Chdir("internal/files"); err != nil {
		t.Fatal(err)
	}
	_, res, _ = seekJSON(t, "files.go:List")
	if res.Target != "internal/files/files.go:List" || len(res.Groups) != 2 || res.Groups[0].Path != "main.go" {
		t.Errorf("in a subdirectory: target %q, %q", res.Target, groupsOf(res))
	}
}

// Errors are envelopes on stdout too, with the text's exit codes; a ref
// that names nothing lists the refs it may have meant.
func TestJSONErrors(t *testing.T) {
	write := refProject(t)
	fails := func(code string, exit int, args ...string) *report.Error {
		t.Helper()
		d, _, got := jsonRun(t, nil, args...)
		if d.Error == nil || d.Error.Code != code || got != exit || d.Result != nil {
			t.Fatalf("seek %v: exit %d, error %+v, want %s and exit %d", args, got, d.Error, code, exit)
		}
		return d.Error
	}
	e := fails(report.CodeNoMatch, 1, "--json", "files.go:Lis")
	wantList(t, "candidates", e.Candidates, []string{"internal/files/files.go:List"})
	if e.Message != `files.go:Lis: no definition named "Lis" in internal/files/files.go` {
		t.Errorf("message %q", e.Message)
	}
	e = fails(report.CodeNoMatch, 1, "--json", "files.go:zzz")
	wantList(t, "all candidates", e.Candidates, []string{"internal/files/files.go:List", "internal/files/files.go:globSet", "internal/files/files.go:globSet.match"})
	e = fails(report.CodeNoMatch, 1, "--json", "lib.cpp:parse#3")
	wantList(t, "ordinals", e.Candidates, []string{"src/lib.cpp:parse#1", "src/lib.cpp:parse#2"})
	e = fails(report.CodeNoMatch, 1, "--json", "main.go:3")
	if !strings.Contains(e.Message, "outside any definition") || len(e.Candidates) != 0 {
		t.Errorf("outside: %+v", e)
	}
	fails(report.CodeNoMatch, 1, "--json", "main.go:99")

	write("a/util.go", "package a\n\nfunc helper() {}\n")
	write("b/util.go", "package b\n\nfunc helper() {}\n")
	e = fails(report.CodeAmbiguous, 1, "--json", "util.go:3")
	wantList(t, "paths", e.Candidates, []string{"a/util.go:3", "b/util.go:3"})

	cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-qm", "init")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %s", out)
	}
	e = fails(report.CodeUsage, 2, "--json", "main.go:run@HEAD")
	if !strings.Contains(e.Message, "recap show HEAD --show 'main.go:run'") {
		t.Errorf("revision: %q", e.Message)
	}

	for _, args := range [][]string{
		{"--json", "--bogus", "x"},
		{"--bogus", "--json", "x"}, // parse stops at --bogus
		{"-b", "x", "--json", "y"},
		{"--json"},
		{"--json", "-l", "-s", "x"},
		{"--json", "x", "nofolder"},
	} {
		e = fails(report.CodeUsage, 2, args...)
		if e.Message == "" {
			t.Errorf("%v: no message", args)
		}
	}

	// --json is a flag, not a pattern or a flag's value
	for _, args := range [][]string{{"-e", "--json"}, {"--", "--json"}, {"-ie", "--json"}, {"-g", "--json", "x"}} {
		if jsonFlag(args) {
			t.Errorf("jsonFlag(%q)", args)
		}
	}
	for _, args := range [][]string{{"-C3", "--json"}, {"-i", "--json"}, {"-b", "5", "--json"}} {
		if !jsonFlag(args) {
			t.Errorf("!jsonFlag(%q)", args)
		}
	}
}

// The line budget doesn't apply to --json unless -b is given; each list is
// capped, and truncated says what was cut while counts keep the totals.
func TestJSONLimits(t *testing.T) {
	write := project(t)
	var b strings.Builder
	b.WriteString("package big\n\nfunc a() {\n")
	for range 150 {
		b.WriteString("\tneedle()\n")
	}
	b.WriteString("}\n\nfunc parse() {}\n\nfunc parse2() { parse() }\n")
	write("big.go", b.String())
	write("other.go", "package big\n\nfunc parse() {}\n")
	write("min.js", "var x = 1;"+strings.Repeat(" ", 3000)+"needle();"+strings.Repeat("y", 3000)+"\n")

	d, res, _ := seekJSON(t, "needle")
	if res.Counts.Hits != 151 || len(res.Groups) != 2 || len(res.Groups[0].Hits) != 150 || d.Truncated != nil {
		t.Errorf("no -b: counts %+v, %q, truncated %v", res.Counts, groupsOf(res), d.Truncated)
	}
	// a minified line is cut around its match
	h := hitAt(t, res, "min.js", 1)
	if len(h.Text) > maxText+2*len("…") || !strings.Contains(h.Text, "needle();") || !strings.HasPrefix(h.Text, "…") || !strings.HasSuffix(h.Text, "…") || h.Col != 3011 {
		t.Errorf("long line: col %d, %d bytes: %.80q…", h.Col, len(h.Text), h.Text)
	}

	d, res, _ = seekJSON(t, "-b", "10", "needle")
	if n := len(res.Groups[0].Hits); n != 10 || len(res.Groups) != 1 || res.Counts.Hits != 151 || d.Truncated == nil || *d.Truncated != (report.Truncated{Shown: 10, Total: 151}) {
		t.Errorf("-b 10: %d hits, counts %+v, truncated %v", n, res.Counts, d.Truncated)
	}
	if d, _, _ = seekJSON(t, "-b", "0", "needle"); d.Truncated != nil {
		t.Errorf("-b 0: truncated %v", d.Truncated)
	}

	old := maxItems
	maxItems = 2
	t.Cleanup(func() { maxItems = old })
	d, res, _ = seekJSON(t, "needle")
	if len(res.Groups) != 1 || len(res.Groups[0].Hits) != 2 || d.Truncated == nil || *d.Truncated != (report.Truncated{Shown: 2, Total: 151}) {
		t.Errorf("cap: %q, truncated %v", groupsOf(res), d.Truncated)
	}
	d, res, _ = seekJSON(t, "-d", "parse")
	if len(res.Definitions) != 2 || res.Counts.Definitions != 2 || d.Truncated != nil {
		t.Errorf("-d under the cap: %q, truncated %v", refsOf(res.Definitions), d.Truncated)
	}
	maxItems = 1
	d, res, _ = seekJSON(t, "-d", "parse")
	if len(res.Definitions) != 1 || res.Counts.Definitions != 2 || d.Truncated == nil || *d.Truncated != (report.Truncated{Shown: 1, Total: 2}) {
		t.Errorf("-d cap: %q, truncated %v", refsOf(res.Definitions), d.Truncated)
	}
	d, res, _ = seekJSON(t, "-l", "needle")
	if len(res.Files) != 1 || res.Counts.Files != 2 || d.Truncated == nil || *d.Truncated != (report.Truncated{Shown: 1, Total: 2}) {
		t.Errorf("-l cap: %q, truncated %v", res.Files, d.Truncated)
	}
}
