package main

import (
	"bufio"
	"bytes"
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
		"  func Repo.Open  L11-18\n    10 // Open opens a repository.  (comment)\n    11 func",
		"    11 func (r *Repo) Open(path string) error {\n",
		"    14 _ = strings.TrimSpace(path) // Open is fine  (comment)",
		`    15 msg := "Open failed"  (string)`,
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
	expect(t, out, "1 definition", "shop/repo.go:11-18  func (r *Repo) Open(path string) error")
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
		"  func Repo.Open  L11-18\n    11- func (r *Repo) Open(path string) error {\n",
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
	expect(t, out, "no hits", "load_map  1 hit, defined at map.js:2 (function)", "loadWorldMap  1 hit, defined at map.js:1 (function)",
)
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
	reject(t, out, "speedMult")
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
	expect(t, out, "src/a.go", "new.go", `in 2 *.go !*_test.go files`)
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
	expect(t, out, "  fn label  L2-4\n    1: /// The label is yellow.  (comment)\n    2- fn label() -> u32 {")
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
