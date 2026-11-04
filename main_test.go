package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// project makes a temporary folder, changes into it, and returns a
// function that writes files there.
func project(t *testing.T) func(name, text string) {
	t.Helper()
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
	return func(name, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func gitInit(t *testing.T) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
}

func seek(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var b, e bytes.Buffer
	w := bufio.NewWriter(&b)
	code := run(args, w, &e, false)
	w.Flush()
	return b.String() + e.String(), code
}

func expect(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in\n%s", w, out)
		}
	}
}

func reject(t *testing.T, out string, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(out, b) {
			t.Errorf("should not contain %q:\n%s", b, out)
		}
	}
}

const goSrc = `package shop

import "strings"

// Repo stores carts.
type Repo struct {
	carts map[string]*Cart
}

// Open opens a repository.
func (r *Repo) Open(path string) error {
	total := 0
	total = 3
	_ = strings.TrimSpace(path) // Open is fine
	msg := "Open failed"
	_ = msg
	return nil
}

func use() {
	r := &Repo{}
	r.Open("x")
}
`

const jsSrc = `import { setSpeed } from "./speed.js";

export function createControls(runtime) {
	function bindSpeed() {
		runtime.setSpeed(0);
	}
	const reset = () => {
		let speed = 1;
		speed += 2;
		setSpeed(speed);
	};
	return { bindSpeed, reset };
}

export function setSpeed(i) {
	return i;
}
`

func TestGroupsAndLabels(t *testing.T) {
	write := project(t)
	write("shop/repo.go", goSrc)
	write("web/controls.js", jsSrc)
	out, code := seek(t, "Open")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	expect(t, out,
		`seek: "Open" (whole word): 5 hits in 1 file`+"\n",
		"shop/repo.go  23 lines\n",
		"  func Repo.Open  L10-18\n    10 // Open opens a repository.  (comment)\n    11 func",
		"    11 func (r *Repo) Open(path string) error {\n",
		"    14   _ = strings.TrimSpace(path) // Open is fine  (comment)",
		`    15   msg := "Open failed"  (string)`,
		"  func use  L20-23",
		`    22 r.Open("x")`,
	)
	out, _ = seek(t, "setSpeed|speed")
	expect(t, out,
		`"setSpeed|speed" (whole words)`,
		"  (top level)\n     1 import { setSpeed } from \"./speed.js\";\n",
		"  function createControls.bindSpeed  L4-6",
		"  const createControls.reset  L7-11",
		"     8 let speed = 1;\n",
		"     9 speed += 2;\n",
		"    15 export function setSpeed(i) {\n",
	)
	// the file with the definition comes first
	if strings.Index(out, "web/controls.js") > strings.Index(out, "shop/") && strings.Contains(out, "shop/") {
		t.Errorf("defining file should come first:\n%s", out)
	}
}

func TestBudgetFallsBackToCounts(t *testing.T) {
	write := project(t)
	var b strings.Builder
	b.WriteString("package big\n\n")
	for i := 0; i < 40; i++ {
		b.WriteString("func f" + itoa(i) + "() {\n\ttick()\n\ttick()\n}\n\n")
	}
	write("big.go", b.String())
	write("other.go", "package big\n\nfunc g() {\n\ttick()\n}\n")
	out, _ := seek(t, "-b", "20", "tick")
	expect(t, out, "big.go  202 lines, 80 hits in 40 places: f0 2, f1 2, f2 2, f3 2, +36 more", "other.go  5 lines\n  func g  L3-5\n    4 tick()")
	out, _ = seek(t, "-b", "60", "tick")
	expect(t, out, "  func f0  L3-6  2 hits: L4, 5")
	out, _ = seek(t, "-c", "tick")
	expect(t, out, "  func f39  L198-201  2 hits: L199, 200")
	reject(t, out, "\ttick()")
	if n := strings.Count(out, "\n"); n > 50 {
		t.Errorf("-c should print one line per definition, got %d lines", n)
	}
}

func TestDefinitionsOnly(t *testing.T) {
	write := project(t)
	write("shop/repo.go", goSrc)
	write("web/controls.js", jsSrc)
	out, code := seek(t, "-d", "Repo.Open")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	expect(t, out, "1 definition", "shop/repo.go:10-18  func (r *Repo) Open(path string) error")
	out, _ = seek(t, "-d", "setSpeed")
	expect(t, out, "web/controls.js:15-17  export function setSpeed(i)", "other hits elsewhere")
	reject(t, out, "controls.js:1-")
	write("web/conf.js", "export const LIMIT = 5;\nfunction f() {\n\tconst LIMIT = 2;\n\treturn LIMIT;\n}\n")
	out, _ = seek(t, "-d", "LIMIT")
	expect(t, out, "1 definition, 2 other hits", "web/conf.js:1  export const LIMIT = 5;") // module level only
	out, _ = seek(t, "-ds", "setSpeed")
	expect(t, out, "  function setSpeed  L15-17\n    15: export function setSpeed(i) {\n    16- \treturn i;\n    17- }")
	out, code = seek(t, "-d", "nothing")
	if code != 1 || !strings.Contains(out, "no definition found") {
		t.Errorf("exit %d:\n%s", code, out)
	}
	// fields, attributes and enum members define their names
	write("model.go", "package m\n\ntype File struct {\n\tLines    int\n\tTopLines []string // changed lines\n}\n\nfunc (f *File) n() int { return len(f.TopLines) }\n")
	write("model.py", "class Cart:\n    count: int = 0\n\n    def add(self):\n        self.count = 1\n")
	write("model.ts", "export class View {\n\treadonly size = 3;\n\trender() {\n\t\treturn this.size;\n\t}\n}\nenum Color {\n\tRed,\n\tGreen,\n}\n")
	out, _ = seek(t, "-d", "TopLines|count|size|Green")
	expect(t, out, "model.go:5  TopLines []string // changed lines", "model.py:2  count: int = 0", "model.ts:2  readonly size = 3;", "model.ts:9  Green,")
	reject(t, out, "model.go:8", "model.py:5", "model.ts:4")
	out, _ = seek(t, "-d", "total")
	expect(t, out, "no definition found", "seek total lists them") // declared inside a function: not a definition
}

func TestBodies(t *testing.T) {
	write := project(t)
	write("shop/repo.go", goSrc)
	out, _ := seek(t, "-s", "TrimSpace")
	expect(t, out,
		"  func Repo.Open  L10-18\n    10- // Open opens a repository.\n    11- func (r *Repo) Open(path string) error {\n",
		"    14: \t_ = strings.TrimSpace(path) // Open is fine",
		"    18- }",
	)
	// long definitions show windows around the hits
	var b strings.Builder
	b.WriteString("package long\n\nfunc Long() {\n")
	for i := 0; i < 200; i++ {
		if i == 100 {
			b.WriteString("\tneedle()\n")
		} else {
			b.WriteString("\tx()\n")
		}
	}
	b.WriteString("}\n")
	write("long.go", b.String())
	out, _ = seek(t, "-s", "needle")
	expect(t, out, "      3- func Long() {\n      …\n    101- \tx()", "    104: \tneedle()", "    107- \tx()\n      …")
}

func TestNoHitsHints(t *testing.T) {
	write := project(t)
	write("map.js", "export function loadWorldMap() {}\nexport function load_map() {}\nconst LoadMapper = 1;\nfunction rebuildFrontline() {}\n")
	out, code := seek(t, "loadMap")
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	expect(t, out, "no hits", "load_map  1 hit, defined at map.js:2 (function)", "loadWorldMap  1 hit, defined at map.js:1 (function)")
	reject(t, out, "LoadMapper") // much less alike than load_map: not shown
	out, _ = seek(t, "rebuildfrontline")
	expect(t, out, "ignoring case: 1 hit in 1 file (rebuildFrontline): seek -i rebuildfrontline")
	out, _ = seek(t, "rebuildFrontLine")
	expect(t, out, "rebuildFrontline  1 hit")
	// names inside a regex get suggestions too; a name with another verb is similar
	write("world.js", "export function invalidateFrontlineField() {}\n")
	out, _ = seek(t, `function\s+rebuildFrontlineField\(`)
	expect(t, out, "invalidateFrontlineField  1 hit, defined at world.js:1 (function)")
	// a two-part name: the same noun with another verb, the same verb; not just its parts
	write("proj.py", "def load_project(p):\n    pass\n\ndef save_settings(s):\n    pass\n\nproject = None\n")
	out, _ = seek(t, "save_project")
	expect(t, out, "load_project  1 hit, defined at proj.py:1 (def)", "save_settings  1 hit, defined at proj.py:4 (def)")
	reject(t, out, "    project  ")
}

func TestPatterns(t *testing.T) {
	write := project(t)
	write("a.js", "const $scope = 1;\nfoo(bar);\nspeedMult = 2;\nlet speed = 3;\nconst colour = 'x';\n")
	out, _ := seek(t, "foo(")
	expect(t, out, `"foo(" (not a valid regex (missing closing ): `+"`foo(`"+`), searched as text)`, "2 foo(bar);")
	out, _ = seek(t, "$scope")
	expect(t, out, "1 const $scope = 1;\n")
	out, _ = seek(t, "speed")
	reject(t, out, "3 speedMult")
	expect(t, out, "+1 more hit inside longer words (speedMult 1): seek --partial speed")
	out, _ = seek(t, "--partial", "speed")
	expect(t, out, "3 speedMult = 2;\n")
	out, _ = seek(t, "colou?r")
	expect(t, out, "const colour = 'x';")
	out, _ = seek(t, "-F", "a.b")
	if !strings.Contains(out, "no hits") {
		t.Errorf("-F a.b is literal:\n%s", out)
	}
	out, _ = seek(t, "-i", "SPEED")
	expect(t, out, "let speed = 3;")
	out, _ = seek(t, "-w", "spe+d")
	reject(t, out, "speedMult")
	expect(t, out, "let speed")
	// a required literal is only a prefilter: alternatives without one still match
	out, _ = seek(t, "zzz|\\$sc")
	expect(t, out, "const $scope")
}

func TestFilesSeen(t *testing.T) {
	write := project(t)
	write(".gitignore", "build/\n*.log\n")
	write("src/a.go", "package a // needle\n")
	write("src/a_test.go", "package a // needle\n")
	write("build/out.go", "package a // needle\n")
	write("debug.log", "needle\n")
	write("notes/todo.txt", "needle\n")
	write("go.sum", "example.com/needle v1.0.0 h1:x\n")
	gitInit(t)
	write("new.go", "package a // needle\n") // untracked
	out, _ := seek(t, "needle")
	expect(t, out, "src/a.go", "new.go", "notes/todo.txt", "src/a_test.go", "go.sum  1 line, lock file, 1 hit: L1")
	reject(t, out, "build/out.go", "debug.log")
	// source first, then tests, then docs, lock files last
	if !(strings.Index(out, "src/a.go") < strings.Index(out, "src/a_test.go") && strings.Index(out, "src/a_test.go") < strings.Index(out, "notes/todo.txt") &&
		strings.Index(out, "notes/todo.txt") < strings.Index(out, "go.sum")) {
		t.Errorf("wrong order:\n%s", out)
	}
	out, _ = seek(t, "-u", "needle")
	expect(t, out, "build/out.go", "debug.log")
	out, _ = seek(t, "-g", "*.go", "-g", "!*_test.go", "needle")
	expect(t, out, "src/a.go", "new.go", `in 2 files matching *.go !*_test.go`)
	reject(t, out, "a_test.go", "todo.txt")
	out, _ = seek(t, "needle", "src")
	expect(t, out, "in src, 2 files")
	out, _ = seek(t, "-l", "needle", "src")
	if out != "src/a.go\nsrc/a_test.go\n" {
		t.Errorf("-l:\n%s", out)
	}
}

func TestPlainFolderSkipsCaches(t *testing.T) {
	write := project(t)
	write("app.py", "needle = 1\n")
	write("node_modules/x/index.js", "needle\n")
	write("__pycache__/app.txt", "needle\n")
	write(".hidden/x.txt", "needle\n")
	out, _ := seek(t, "needle")
	expect(t, out, "app.py", "1 needle = 1\n")
	reject(t, out, "node_modules", "__pycache__", ".hidden")
}

func TestBadArguments(t *testing.T) {
	project(t)
	for _, args := range [][]string{{}, {"-x", "a"}, {"-C"}, {"-b", "lots", "a"}, {"a", "missing/"}, {"-l", "-s", "a"}, {"a", "b", "-e", "c"}} {
		if out, code := seek(t, args...); code != 2 {
			t.Errorf("seek %v: exit %d, want 2:\n%s", args, code, out)
		}
	}
	if out, code := seek(t, "--version"); code != 0 || !strings.HasPrefix(out, "seek ") {
		t.Errorf("--version: %d %s", code, out)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// Text in docstrings and multi-line template strings is a string, and
// definitions in it aren't definitions.
func TestMultilineStrings(t *testing.T) {
	write := project(t)
	write("m.py", "\"\"\"Module.\n\ndef fake():  # an example\n\"\"\"\n\n\ndef real():\n    s = '''\n    fake text\n    '''\n    return s\n")
	write("t.ts", "const page = `\n  function fake() {}\n`;\nexport function realTs() { return page; }\n")
	out, _ := seek(t, "-d", "fake|real|realTs")
	expect(t, out, "m.py:7-11  def real():", "t.ts:4  export function realTs()")
	reject(t, out, "m.py:3", "t.ts:2")
	out, _ = seek(t, "fake")
	expect(t, out, "3 def fake():  # an example  (string)", "9 fake text  (string)", "2 function fake() {}  (string)")
}

// Lock, minified and long-line files stay on one line, with a snippet
// around a hit when there are only one or two.
func TestOneLineFiles(t *testing.T) {
	write := project(t)
	write("long.txt", strings.Repeat("x", 6000)+" needle\n")
	write("app.min.js", "var needle=1;"+strings.Repeat("a", 6000)+"\n")
	write("go.sum", "a needle 1\nb needle 2\nc needle 3\n")
	out, _ := seek(t, "needle")
	expect(t, out, "long.txt  1 line, long lines, 1 hit: L1 …xxxxxxxxxxxxxxxxxxxxxxxxxxxxx needle",
		"app.min.js  1 line, minified, 1 hit (1 declaration): L1 var needle=1;aaaa", "go.sum  3 lines, lock file, 3 hits: L1, 2, 3")
}

// -s starts a definition at its doc comment when the hit is there.
func TestBodyIncludesDocCommentHit(t *testing.T) {
	write := project(t)
	write("a.rs", "/// The label is yellow.\nfn label() -> u32 {\n    1\n}\n")
	out, _ := seek(t, "-s", "yellow")
	expect(t, out, "  fn label  L1-4\n    1: /// The label is yellow.  (comment)\n    2- fn label() -> u32 {")
}

// A pattern of thousands of names finishes quickly, and the header doesn't
// repeat it in full (found by the first review round).
func TestHugeWordList(t *testing.T) {
	write := project(t)
	write("a.go", "package a\n\nfunc fn42() {}\n\nfunc other() { fn42() }\n")
	var ws []string
	for i := 0; i < 3000; i++ {
		ws = append(ws, "fn"+itoa(i))
	}
	start := time.Now()
	out, _ := seek(t, strings.Join(ws, "|"))
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
	expect(t, out, "(19889 characters)", "2 hits in 1 file", "  func other  L5\n    5 func other() { fn42() }")
	if len(out) > 1000 {
		t.Errorf("output too long: %d bytes", len(out))
	}
	out, _ = seek(t, "-i", strings.Join(ws, "|")+"|FN42")
	expect(t, out, "2 hits in 1 file")
}

// Regressions from the first review round: anchors and empty matches,
// non-ASCII names, globs.
func TestFirstRoundPatterns(t *testing.T) {
	write := project(t)
	write("a.go", "package a\n\nfunc A() {}\n\nfunc B() {}\n")
	write("über.py", "def über_limit():\n    return 1\n\nxlimit = 1\n")
	out, _ := seek(t, "-c", "^$", "a.go")
	expect(t, out, "2 hits in 1 file", "L2, 4")
	out, _ = seek(t, "-c", "$", "a.go")
	expect(t, out, "5 hits in 1 file") // not 6: no line after the last line break
	out, _ = seek(t, "^func", "a.go")
	expect(t, out, "2 hits in 1 file")
	out, _ = seek(t, "ber_limit")
	expect(t, out, "no hits", "über_limit  1 hit, defined at über.py:1 (def)")
	out, _ = seek(t, "-d", "über_limit")
	expect(t, out, "über.py:1-2  def über_limit():")
	if _, code := seek(t, "-g", "[", "limit"); code != 2 {
		t.Errorf("an unclosed [ in a glob is an error")
	}
	out, _ = seek(t, "-l", "-g", "*.[gp][oy]", "-g", "!*.p[!o]", "func|def")
	if out != "a.go\n" {
		t.Errorf("glob classes: %q", out)
	}
}

// Labels, from the first review round: code inside interpolations, char
// literals, a code hit after a string, block comments over several lines.
func TestFirstRoundLabels(t *testing.T) {
	write := project(t)
	write("a.ts", "const url = `https://x/${getCode(c)}.webp`;\nconst msg = `getCode is ${n}`;\nconst u = \"getCode\"; getCode();\n/*\n getCode was renamed\n*/\nfunction getCode(c) { return c; }\n")
	write("m.py", "class Cart:\n    limit = 1\n\n    def label(self):\n        return f\"{self.limit} items\"\n")
	write("r.rs", "fn f() {\n    let q = '\"'; let v = target;\n}\n")
	out, _ := seek(t, "getCode")
	expect(t, out, "1 const url = `https://x/${getCode(c)}.webp`;\n", "2 const msg = `getCode is ${n}`;  (string)",
		"3 const u = \"getCode\"; getCode();\n", "  function getCode  L4-7\n    5  getCode was renamed  (comment)")
	out, _ = seek(t, "limit", "m.py")
	expect(t, out, "5 return f\"{self.limit} items\"\n")
	out, _ = seek(t, "target", "r.rs")
	expect(t, out, "let v = target;\n")
}

// -d, from the first review round.
func TestFirstRoundDefinitions(t *testing.T) {
	write := project(t)
	write("V.java", "class Oracle {\n    private final Sampler sampler;\n    public Sampler compileSampler(int x) {\n        return sampler;\n    }\n}\n")
	write("e.rs", "impl fmt::Display for DecodeError {\n    fn fmt(&self, f: &mut fmt::Formatter) -> fmt::Result {\n        Ok(())\n    }\n}\n")
	write("guide.md", "# Guide\n\n```go\nfunc limitOf() int { return 10 }\n```\n")
	write("settings.py", "class AppSettings:\n    recent_projects: list = []\n\n\ndef load_settings():\n    pass\n")
	write("vendor/lib/x.go", "package lib\n\nfunc Vendored() {}\n")
	write("main.js", "const { performTick } = createTick();\nexport function createTick() {\n\tfunction performTick() {}\n\treturn { performTick };\n}\n")
	out, _ := seek(t, "-d", "Sampler|sampler|compileSampler")
	expect(t, out, "V.java:2  private final Sampler sampler;", "V.java:3-5  public Sampler compileSampler(int x)")
	reject(t, out, "3 definitions") // the types in the field and the method aren't definitions of Sampler
	out, _ = seek(t, "-d", "fmt")
	expect(t, out, "1 definition", "e.rs:2-4  fn fmt(")
	out, _ = seek(t, "-d", "impl.*Display for")
	expect(t, out, "e.rs:1-5  impl fmt::Display for DecodeError")
	out, _ = seek(t, "-d", "limitOf")
	expect(t, out, "no definition found")
	out, _ = seek(t, "-d", "--partial", "recent")
	expect(t, out, "settings.py:2  recent_projects: list = []")
	out, _ = seek(t, "-d", "Vendored")
	expect(t, out, "vendor/lib/x.go:3  func Vendored()  (vendored)")
	out, _ = seek(t, "-d", "load")
	expect(t, out, "no definition found", "definitions whose names contain it: load_settings (settings.py:5): seek -d --partial load")
	out, _ = seek(t, "-d", "Nope.performTick")
	expect(t, out, "seek -d: \"Nope.performTick\"", "seek performTick lists them")
	out, _ = seek(t, "-d", "performTick")
	expect(t, out, "main.js:3  function performTick()")
	reject(t, out, "main.js:1") // destructuring binds, it doesn't define
}

// Output, from the first review round: what the budget and whole words
// leave out, CRLF, ranking, skipped files, flags.
func TestFirstRoundOutput(t *testing.T) {
	write := project(t)
	var b strings.Builder
	b.WriteString("package big\n\n")
	for i := 0; i < 6; i++ {
		b.WriteString("func f" + itoa(i) + "() {\n")
		for k := 0; k < 30; k++ {
			b.WriteString("\tx()\n")
		}
		b.WriteString("\tretreat()\n\tretreatVector()\n}\n\n")
	}
	write("big.go", b.String())
	write("crlf.py", "def windows():\r\n    return retreat\r\n")
	write("scripts/retreat-smoke.mjs", "retreat();\n")
	write("src/ai.js", "export function plan() {\n  retreat();\n}\n")
	write("blob.txt", "retreat\x00binary\n")
	out, _ := seek(t, "retreat")
	expect(t, out, "+6 more hits inside longer words (retreatVector 6): seek --partial retreat", "crlf.py  2 lines, CRLF line endings")
	if strings.Index(out, "src/ai.js") > strings.Index(out, "scripts/retreat-smoke.mjs") {
		t.Errorf("smoke scripts rank after source:\n%s", out)
	}
	out, _ = seek(t, "-s", "-b", "40", "retreat", "big.go")
	expect(t, out, "(-s: 1 of 1 file shows only part of its definitions to fit the budget; raise -b")
	out, _ = seek(t, "-s", "-b", "90", "retreat", "big.go")
	expect(t, out, "      3- func f0() {\n      …\n     31- \tx()", "(-s: 1 of 1 file shows only part of its definitions")
	out, _ = seek(t, "retreat", "blob.txt")
	expect(t, out, "not searched: blob.txt is binary")
	out, code := seek(t, "-retreat")
	if code != 2 || !strings.Contains(out, "seek -e -retreat") {
		t.Errorf("a pattern starting with - should point to -e: %d %s", code, out)
	}
	out, _ = seek(t, "-l", "-0", "plan")
	if out != "src/ai.js\x00" {
		t.Errorf("-l -0: %q", out)
	}
	write("twice.js", "retreatX(); retreatX();\n")
	out, _ = seek(t, "retreatXY")
	expect(t, out, "retreatX  1 hit") // lines, like the search counts
}

// A nested repository isn't searched; when nothing matches, seek says so.
func TestNestedRepositoryNote(t *testing.T) {
	write := project(t)
	write("a.go", "package a\n")
	gitInit(t)
	write("sub/inner.go", "package inner\n\nfunc innerOnly() {}\n")
	cmd := exec.Command("git", "init", "-q", "sub")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s", out)
	}
	out, _ := seek(t, "innerOnly")
	expect(t, out, "no hits", "not searched: 1 repository inside: sub/ (give it as a path to search it)")
	out, _ = seek(t, "innerOnly", "sub")
	expect(t, out, "1 hit in 1 file")
}

// Code in callbacks and setter objects belongs to them, not to the top level.
func TestJavaScriptCallbacks(t *testing.T) {
	write := project(t)
	write("w.js", "_worker.onmessage = (evt) => {\n\tconst field = evt.data;\n\tuse(field);\n};\nconst setters = {\n\tfield: (v) => {\n\t\tfield = v;\n\t},\n};\n")
	out, _ := seek(t, "field")
	expect(t, out, "  function _worker.onmessage  L1-4", "  method field  L6-8")
}

// -s doesn't print a nested definition a second time inside the one
// around it (found while recording the preview).
func TestBodiesDontRepeatNested(t *testing.T) {
	write := project(t)
	write("u.js", "export function make(world) {\n\tfunction should(unit) {\n\t\treturn unit.x > 1;\n\t}\n\n\tfunction step(unit) {\n\t\tif (should(unit)) {\n\t\t\tunit.x = 0;\n\t\t}\n\t}\n\n\treturn { step, should };\n}\n")
	out, _ := seek(t, "-s", "should")
	if n := strings.Count(out, "return unit.x > 1;"); n != 1 {
		t.Errorf("printed %d times:\n%s", n, out)
	}
	expect(t, out, "  function make  L1-13\n     1- export function make(world) {\n     …\n     9- \t\t}\n    10- \t}\n    11-\n    12: \treturn { step, should };")
}

// From the second review round: hits in another case or in longer words
// are named right under the header (also with -l, on stderr); one range per
// definition; dots are regex dots; impls answer -d for their trait.
func TestSecondRoundRegressions(t *testing.T) {
	write := project(t)
	write("u.js", "// Retreat when outnumbered.\nfunction plan(u) {\n\tif (u.retreat) {\n\t\tu.retreatVector = 1;\n\t}\n\tu.state = \"RETREATING\";\n}\n")
	write("lib.rs", "pub trait Shape {\n    fn area(&self) -> f64;\n}\n\nmod tests {\n    struct Sq;\n    impl super::Shape for Sq {\n        fn area(&self) -> f64 {\n            1.0\n        }\n    }\n}\n")
	write("k.go", "package k\n\nvar (\n\tDefaultLimit = 10\n\tmaxRetries   = 3\n)\n\n// Load reads it.\nfunc Load() int { return DefaultLimit }\n")
	write("re.js", "const re = /`/g;\nfunction foo() {\n\treturn bar_word;\n}\n")
	out, _ := seek(t, "retreat")
	lines := strings.Split(out, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[1], "+3 more hits in another case or inside longer words (") || !strings.HasSuffix(lines[1], ": seek -i --partial retreat") {
		t.Errorf("the note belongs right under the header:\n%s", out)
	}
	var b, e bytes.Buffer
	w := bufio.NewWriter(&b)
	run([]string{"-l", "retreat"}, w, &e, false)
	w.Flush()
	if b.String() != "u.js\n" || !strings.Contains(e.String(), "+3 more hits") {
		t.Errorf("-l: stdout %q, stderr %q", b.String(), e.String())
	}
	// one range per definition, whether or not the doc comment has a hit
	out, _ = seek(t, "DefaultLimit", "k.go")
	expect(t, out, "  func Load  L8-9")
	out, _ = seek(t, "-d", "Load")
	expect(t, out, "k.go:8-9  func Load() int")
	out, _ = seek(t, "-d", "DefaultLimit|maxRetries")
	expect(t, out, "k.go:4  DefaultLimit = 10", "k.go:5  maxRetries   = 3")
	out, _ = seek(t, "-c", "Default.imit", "k.go")
	expect(t, out, "seek: \"Default.imit\" (whole word) in k.go: 2 hits")
	out, _ = seek(t, "-d", "Shape")
	expect(t, out, "lib.rs:1-3  pub trait Shape", "lib.rs:7-11  impl super::Shape for Sq")
	out, _ = seek(t, "-l", "-d", "Shape|Load")
	if out != "k.go\nlib.rs\n" {
		t.Errorf("-l -d: %q", out)
	}
	out, _ = seek(t, "bar_word", "re.js")
	expect(t, out, "  function foo  L2-4\n    3 return bar_word;\n")
	out, _ = seek(t, "--writes", "retreatVector|DefaultLimit")
	expect(t, out, "u.retreatVector = 1;", "DefaultLimit = 10")
	reject(t, out, "return DefaultLimit")
	if _, code := seek(t, "retreat", "vector"); code != 2 {
		t.Errorf("a second word that isn't a path is an error")
	}
	out, _ = seek(t, "retreat", "vector")
	expect(t, out, "join them: 'retreat|vector'")
}

// In a long definition, each hit comes after the line that opens its block.
func TestBlockOpeners(t *testing.T) {
	write := project(t)
	var b strings.Builder
	b.WriteString("func big() {\n")
	for i := 0; i < 200; i++ {
		b.WriteString("\tx()\n")
	}
	b.WriteString("\tif ready {\n\t\tneedle()\n\t}\n}\n")
	write("b.go", "package b\n\n"+b.String())
	out, _ := seek(t, "needle")
	expect(t, out, "  func big  L3-207\n    204- if ready {\n    205:   needle()")
}

// Found while finishing the second round: an empty alternative crashed,
// code in a multi-line template's ${…} was labelled a string, folders of
// binary files gave a bare "no hits", and -d dropped -c and -C silently.
func TestSecondRoundFollowups(t *testing.T) {
	write := project(t)
	write("k.go", "package k\n\nvar (\n\tLimit = 10\n)\n\nfunc f() int { return Limit }\n")
	write("chat.js", "function render(name) {\n\treturn `\n\t\t<span>${name}</span>\n\t\t<b>name</b>\n\t`;\n}\n")
	write("assets/a.png", "\x89PNG\x00\x00")
	write("assets/b.png", "\x89PNG\x00\x00")
	for _, args := range [][]string{{"Limit|"}, {"-c", "a||b"}, {"-d", "Limit|"}, {"--writes", "Limit|"}} {
		out, code := seek(t, args...)
		if code != 0 {
			t.Errorf("%v: exit %d\n%s", args, code, out)
		}
	}
	out, _ := seek(t, "Limit|")
	expect(t, out, "(matches the empty string, so every line)")
	out, _ = seek(t, "name", "chat.js")
	expect(t, out, "<span>${name}</span>\n", "<b>name</b>  (string)")
	out, _ = seek(t, "zzqq", "assets")
	expect(t, out, "no hits", "not searched: 2 binary files")
	out, _ = seek(t, "zzqq", "assets/a.png")
	expect(t, out, "not searched: assets/a.png is binary")
	reject(t, out, "binary files")
	for _, args := range [][]string{{"-d", "-c", "Limit"}, {"-d", "-C", "2", "Limit"}} {
		if out, code := seek(t, args...); code != 2 || !strings.Contains(out, "-d lists definitions one line each") {
			t.Errorf("%v: exit %d\n%s", args, code, out)
		}
	}
}

// From the final review round: with -i the note names only words -i doesn't
// already match and keeps -i in its command; -l still says what it skipped;
// --writes leaves out getters and destructuring keys; a Latin-1 letter
// ends no word.
func TestFinalRoundRegressions(t *testing.T) {
	write := project(t)
	write("a.js", "const LIMIT = 1;\nlet limit = 2;\nfunction f(o) {\n\tconst { limit: l } = o;\n\treturn limitOf(l);\n}\nclass C {\n\tget limit() { return 1; }\n}\n")
	write("img/x.png", "\x89PNG\x00\x00")
	write("latin1.txt", "limit caf\xe9 na\xefve\n")
	out, _ := seek(t, "-i", "limit")
	expect(t, out, "+1 more hit inside longer words (limitOf 1): seek -i --partial limit")
	reject(t, out, "LIMIT 1")
	var b, e bytes.Buffer
	w := bufio.NewWriter(&b)
	if code := run([]string{"-l", "zzqq"}, w, &e, false); code != 1 {
		t.Errorf("-l with no hits: exit %d", code)
	}
	w.Flush()
	if b.String() != "" || !strings.Contains(e.String(), "not searched: 1 binary file") {
		t.Errorf("-l: stdout %q, stderr %q", b.String(), e.String())
	}
	out, _ = seek(t, "--writes", "limit", "a.js")
	expect(t, out, "let limit = 2;")
	reject(t, out, "const { limit: l }", "get limit()", "return limitOf")
	out, code := seek(t, "caf", "latin1.txt")
	if code != 1 || !strings.Contains(out, "no hits") {
		t.Errorf("caf is not a whole word in caf\\xe9: exit %d\n%s", code, out)
	}
}

// More from the final round (Opus): the note leaves out words the pattern
// matches itself; --writes says what it left out; ^$ isn't "every line";
// an empty file has no line 1; -C shrinks its context to fit rather than
// dropping the hit lines; a qualified name with no text hits points to its
// definition; skipped binary files are named; -l paths never look like
// options; a loop counter defines nothing.
func TestFinalRoundOpus(t *testing.T) {
	write := project(t)
	write("k.go", "package k\n\ntype Repo struct{}\n\n// Changes lists them.\nfunc (r *Repo) Changes() int { return 0 }\n\nvar limit = 1\nvar limitOf = 2\n")
	write("w.js", "let target = 1;\nfor (const t of list) {}\nuse(target);\nfor (let tick = 0; tick < 3; tick++) {}\nconst s = `\n  more ${target + 1} text target\n`;\n")
	write("empty.go", "")
	write("-rf.js", "target();\n")
	write("bin/a.png", "\x89PNG\x00\x00")
	var big strings.Builder
	big.WriteString("package big\n\nfunc Big() {\n")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&big, "\tx%d()\n", i)
		if i%60 == 30 {
			big.WriteString("\tneedle()\n")
		}
	}
	big.WriteString("}\n")
	write("big.go", big.String())

	out, _ := seek(t, "lim.t", "k.go")
	expect(t, out, "+1 more hit inside longer words (limitOf 1)")
	out, _ = seek(t, "--writes", "target", "w.js")
	expect(t, out, "1 hit that set it in 1 file", "+2 more hits that don't set it: seek target")
	out, _ = seek(t, "--writes", "list", "w.js")
	expect(t, out, "no hits that set it (1 other hit: seek list)")
	reject(t, out, "ignoring case", "inside longer words")
	out, _ = seek(t, "^$", "k.go")
	reject(t, out, "every line")
	out, code := seek(t, "^", "empty.go")
	if code != 1 {
		t.Errorf("an empty file has no lines: exit %d\n%s", code, out)
	}
	out, _ = seek(t, "-C", "40", "needle", "big.go")
	expect(t, out, ": \tneedle()", "shows less context around its hits")
	out, _ = seek(t, "Repo.Changes")
	expect(t, out, "no hits", "defined at k.go:6 (func): seek -d Repo.Changes")
	out, _ = seek(t, "zzqq")
	expect(t, out, "not searched: 1 binary file (bin/a.png)")
	out, _ = seek(t, "-l", "target")
	expect(t, out, "./-rf.js\n")
	out, _ = seek(t, "-d", "tick", "w.js")
	expect(t, out, "no definition found")
	out, _ = seek(t, "target", "w.js")
	expect(t, out, "more ${target + 1} text target\n")
}
