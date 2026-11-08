// Package group puts a file's hits under the definitions that contain them
// and labels each hit: definition, declaration, assignment, import,
// comment or string.
package group

import (
	"bytes"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Vaspyyy/prism/outline"
	"github.com/Vaspyyy/prism/ref"
	"github.com/Vaspyyy/seek/internal/search"
)

// Hit is a matching line with what kind of occurrence it is ("" for a
// plain use).
type Hit struct {
	search.Hit
	Label string
}

// Group is the hits inside one definition, or outside any (Sym < 0).
type Group struct {
	Sym       int // index into File.Syms, or -1
	Kind      string
	Name      string // qualified: Type.method
	Ordinal   int    // N in "Name#N" when the file defines Name more than once, else 0
	Sig       string
	Line, End int // from the defining line (not the doc comment) to the last
	Doc       int // first line of its doc comment and decorators, if it has any (else 0)
	Hits      []Hit
}

// RefName is the name as a ref writes it after the path: Name, or Name#N
// when the file defines Name more than once.
func (g *Group) RefName() string { return refName(g.Name, g.Ordinal) }

func refName(name string, ordinal int) string {
	return ref.Symbol("", name, ordinal).String()
}

// First is the first line to read: the doc comment's if it has one.
func (g *Group) First() int {
	if g.Doc > 0 && g.Doc < g.Line {
		return g.Doc
	}
	return g.Line
}

// File is a searched file with its hits grouped.
type File struct {
	*search.File
	Syms     []outline.Symbol
	Outlined bool
	Groups   []*Group // in line order; the top-level group first if there is one
	Defs     []Def    // definitions among the hits
	ords     []int    // the ordinal each symbol needs in a ref (see ordinal)
}

// Def is a definition of what was searched for.
type Def struct {
	Kind, Name, Sig string
	Ordinal         int // as in Group
	Line, End       int
	Outline         bool // found by the outline rules (not only by a keyword on its line)
}

// RefName is the name as a ref writes it after the path (see Group).
func (d Def) RefName() string { return refName(d.Name, d.Ordinal) }

// Count is the number of hits.
func (f *File) Count() int {
	n := 0
	for _, g := range f.Groups {
		n += len(g.Hits)
	}
	return n
}

// Labelled counts the hits with a label.
func (f *File) Labelled(label string) int {
	n := 0
	for _, g := range f.Groups {
		for _, h := range g.Hits {
			if h.Label == label {
				n++
			}
		}
	}
	return n
}

// Symbols is the outline of a file as Build sees it, so a symbol's index
// is the same in both: what the outline rules find, without definitions
// inside docstrings and template strings. nil if the file type has no
// outline rules.
func Symbols(path string, data []byte) []outline.Symbol {
	if !outline.Supported(path) || search.Kind(path, data) == "minified" {
		return nil
	}
	inText, _ := multiline(path, data)
	return realSymbols(outline.Parse(path, data, 1<<20), inText)
}

// realSymbols drops the definitions the outline found inside a docstring
// or a template string: they aren't real.
func realSymbols(syms []outline.Symbol, inText map[int]string) []outline.Symbol {
	if len(inText) == 0 {
		return syms
	}
	var keep []outline.Symbol
	for _, s := range syms {
		if inText[s.Line] == "" {
			keep = append(keep, s)
		}
	}
	if len(keep) < len(syms) {
		return reparent(keep)
	}
	return syms
}

// Build groups f's hits.
func Build(f *search.File, p *search.Pattern) *File {
	out := &File{File: f}
	inText, inCode := multiline(f.Path, f.Data)
	if outline.Supported(f.Path) && f.Kind != "minified" {
		out.Syms = realSymbols(outline.Parse(f.Path, f.Data, 1<<20), inText)
		out.Outlined = true
	}
	st := styleOf(f.Path)
	byKey := map[int]*Group{}
	var lines []string
	var blocks map[int]string
	for _, h := range f.Hits {
		si := innermost(out.Syms, h.Line)
		if l := inText[h.Line]; l != "" {
			if l == "comment" && len(out.Syms) > 0 {
				if lines == nil {
					lines = strings.Split(string(f.Data), "\n")
				}
				if d := docOf(out.Syms, si, h.Line, lines, st, inText); d >= 0 {
					si = d // in the doc comment of the definition below
				}
			}
			if l == "string" && anyWithin(h.Spans, inCode[h.Line]) {
				l = "" // code in a template's ${…}: one hit in code decides
			}
			g := groupFor(out, byKey, si)
			g.Hits = append(g.Hits, Hit{Hit: h, Label: l})
			continue
		}
		if len(out.Syms) > 0 && (commentLine(h.Text, st) || decorator(h.Text)) {
			if lines == nil {
				lines = strings.Split(string(f.Data), "\n")
			}
			if d := docOf(out.Syms, si, h.Line, lines, st, inText); d >= 0 {
				si = d // a doc comment belongs to the definition below it
			}
		}
		g := groupFor(out, byKey, si)
		hit := Hit{Hit: h}
		field := ""
		if si >= 0 {
			field = isField(out.Syms[si], h, st.typed)
		}
		if field != "" {
			hit.Label = "definition"
			out.Defs = append(out.Defs, Def{Kind: "field", Name: g.Name + "." + field,
				Sig: clip(strings.TrimSpace(h.Text), 110), Line: h.Line, End: h.Line})
		} else if si >= 0 && isDef(out.Syms[si], h, p) {
			hit.Label = "definition"
			s := out.Syms[si]
			out.Defs = append(out.Defs, Def{Kind: s.Kind, Name: g.Name, Ordinal: g.Ordinal, Sig: s.Sig, Line: s.Line, End: s.End, Outline: true})
		} else {
			hit.Label = classify(h, st)
			// a definition by keyword, or a variable declared outside any
			// definition (module level), defines what was searched for
			if blocks == nil {
				if lines == nil {
					lines = strings.Split(string(f.Data), "\n")
				}
				blocks = goBlocks(f.Path, lines)
				if blocks == nil {
					blocks = map[int]string{}
				}
			}
			if len(h.Spans) == 0 { // an empty match (limit|) names nothing
				g.Hits = append(g.Hits, hit)
				continue
			}
			a, b := widen(h.Text, h.Spans[0][0], h.Spans[0][1])
			if k := blocks[h.Line]; k != "" && si < 0 && strings.TrimSpace(h.Text[:a]) == "" {
				hit.Label = "definition" // a member of a var ( … ) or const ( … ) block
				out.Defs = append(out.Defs, Def{Kind: k, Name: h.Text[a:b], Sig: clip(strings.TrimSpace(h.Text), 110), Line: h.Line, End: h.Line})
			} else if hit.Label == "definition" || si < 0 && topLevelDecl(h, hit.Label, st) {
				out.Defs = append(out.Defs, Def{Kind: declKind(h), Name: h.Text[a:b], Sig: clip(strings.TrimSpace(h.Text), 110), Line: h.Line, End: h.Line})
			}
		}
		g.Hits = append(g.Hits, hit)
	}
	// -s shows a definition's doc comment too (its range starts at the
	// defining line, as compilers and editors report it)
	if len(out.Groups) > 0 && out.Outlined {
		if lines == nil {
			lines = strings.Split(string(f.Data), "\n")
		}
		for _, g := range out.Groups {
			if g.Sym >= 0 {
				if d := docStart(lines, g.Line, st, inText); d < g.Line {
					g.Doc = d
				}
			}
		}
	}
	// top level first, then by line
	for i, g := range out.Groups {
		if g.Sym < 0 && i > 0 {
			copy(out.Groups[1:i+1], out.Groups[:i])
			out.Groups[0] = g
			break
		}
	}
	return out
}

// Whole is a group for the definition Syms[i] with every hit inside it:
// in its doc comment, its own lines and the definitions nested in it. It
// is for showing the definition itself, as a ref names it.
func (f *File) Whole(i int) *Group {
	s := f.Syms[i]
	g := &Group{Sym: i, Kind: s.Kind, Name: outline.Qualified(f.Syms, i), Ordinal: f.ordinal(i), Sig: s.Sig, Line: s.Line, End: s.End}
	inText, _ := multiline(f.Path, f.Data)
	if d := docStart(strings.Split(string(f.Data), "\n"), s.Line, styleOf(f.Path), inText); d < s.Line {
		g.Doc = d
	}
	for _, o := range f.Groups {
		for _, h := range o.Hits {
			if h.Line >= g.First() && h.Line <= max(g.End, g.Line) {
				g.Hits = append(g.Hits, h)
			}
		}
	}
	slices.SortFunc(g.Hits, func(a, b Hit) int { return a.Line - b.Line })
	return g
}

// ordinal is the N a ref to Syms[i] needs ("parse#2"), or 0 when its name
// is unique in the file. It counts over the whole outline, not just the
// definitions shown, so it doesn't depend on what was searched for.
func (f *File) ordinal(i int) int {
	if f.ords == nil {
		f.ords = ref.Ordinals(ref.Targets(f.Path, f.Syms))
	}
	return f.ords[i]
}

// docOf is the definition that the comment on line n documents: the next
// one, if only comment and decorator lines lie between them, and it's
// inside the same definition as the comment. -1 if there's none.
func docOf(syms []outline.Symbol, in, n int, lines []string, st style, inText map[int]string) int {
	for i, s := range syms {
		if s.Line <= n {
			continue
		}
		if s.Parent != in {
			return -1
		}
		for k := n + 1; k < s.Line; k++ {
			if k > len(lines) || !commentLine(lines[k-1], st) && !decorator(lines[k-1]) && inText[k] != "comment" {
				return -1
			}
		}
		return i
	}
	return -1
}

func commentLine(l string, st style) bool {
	t := strings.TrimSpace(l)
	if t == "" {
		return false
	}
	for _, m := range st.line {
		if strings.HasPrefix(t, m) {
			return true
		}
	}
	return st.block && (strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*"))
}

func decorator(l string) bool {
	t := strings.TrimSpace(l)
	return strings.HasPrefix(t, "@") || strings.HasPrefix(t, "#[")
}

// topLevelDecl: a declaration (const x = …) or, in Python and other
// languages without a keyword, an assignment at the start of an unindented line.
func topLevelDecl(h search.Hit, label string, st style) bool {
	if !st.code {
		return false
	}
	switch label {
	case "declaration":
		// const { a } = b binds a name from something defined elsewhere, and
		// for (let i = …) a loop counter
		before := h.Text[:h.Spans[0][0]]
		return h.Text == strings.TrimLeft(h.Text, " \t") && !strings.ContainsAny(before, "{[(")
	case "assignment":
		return st.fstrings && h.Spans[0][0] == 0 // Python: NAME = … at module level
	}
	return false
}

var kindRe = regexp.MustCompile(`\b(function\*?|def|fn|func|class|struct|enum|interface|trait|type|module|namespace|record|object|union|impl|const|let|var|val|static|local|global)\b`)

// declKind is the keyword that defines a hit's name: const, class, def, …
func declKind(h search.Hit) string {
	if m := kindRe.FindAllString(h.Text[:h.Spans[0][0]], -1); len(m) > 0 {
		return m[len(m)-1]
	}
	return "variable"
}

var (
	containers  = map[string]bool{"type": true, "struct": true, "class": true, "interface": true, "enum": true, "trait": true, "union": true, "record": true, "object": true}
	fieldBefore = regexp.MustCompile(`^\s*(?:(?:pub(?:\([^)]*\))?|public|private|protected|internal|readonly|static|final|val|var|let|const|declare|override|abstract|mut|required|late)\s+)*$`)
	fieldAfter  = regexp.MustCompile(`^(?:\s+[\w*\[\]<>.&(]|\s*[?!]?\s*:[^:]|\s*=[^=>]|\s*[,;]|\s*$|\s*\(|\s*\{)`)
)

func groupFor(out *File, byKey map[int]*Group, si int) *Group {
	g := byKey[si]
	if g == nil {
		g = &Group{Sym: si}
		if si >= 0 {
			s := out.Syms[si]
			g.Kind, g.Name, g.Sig, g.Line, g.End = s.Kind, outline.Qualified(out.Syms, si), s.Sig, s.Line, s.End
			g.Ordinal = out.ordinal(si)
		}
		byKey[si] = g
		out.Groups = append(out.Groups, g)
	}
	return g
}

// reparent recomputes parents after symbols were dropped.
func reparent(syms []outline.Symbol) []outline.Symbol {
	for i := range syms {
		syms[i].Parent = -1
		for j := i - 1; j >= 0; j-- {
			if syms[j].Line < syms[i].Line && syms[j].End >= syms[i].End && syms[j].End > syms[j].Line {
				syms[i].Parent = j
				break
			}
		}
	}
	return syms
}

// multiline labels the lines inside strings and comments that span lines:
// Python's triple-quoted strings (docstrings), backtick strings in
// JavaScript, TypeScript and Go, and /* … */ comments in C-like languages.
// The opening line isn't labelled (code may come before the mark); the
// lines after it up to the closing one are. The second map holds the
// ${…} parts of those lines in a template literal, which are code.
func multiline(path string, data []byte) (map[int]string, map[int][][2]int) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	type mark struct{ open, close, label string }
	block := mark{"/*", "*/", "comment"}
	var marks []mark
	hash, jsLike := false, false
	switch ext {
	case "py", "pyi":
		marks, hash = []mark{{`"""`, `"""`, "string"}, {`'''`, `'''`, "string"}}, true
	case "js", "mjs", "cjs", "ts", "tsx", "jsx", "mts", "cts", "go":
		marks = []mark{{"`", "`", "string"}, block}
		jsLike = ext != "go"
	case "c", "h", "cc", "cpp", "cxx", "hpp", "hh", "java", "cs", "rs", "kt", "kts", "scala", "swift", "css", "scss", "less", "php", "dart", "zig", "glsl", "wgsl", "hlsl":
		marks = []mark{block}
	default:
		return nil, nil
	}
	var in map[int]string
	var code map[int][][2]int
	var open *mark
	n := 0
	for len(data) > 0 {
		n++
		line := data
		if end := bytes.IndexByte(data, '\n'); end >= 0 {
			line, data = data[:end], data[end+1:]
		} else {
			data = nil
		}
		if open != nil {
			if in == nil {
				in = map[int]string{}
			}
			in[n] = open.label
		}
		s := string(line)
		for i := 0; i < len(s); {
			if open != nil {
				switch {
				case s[i] == '\\' && open.open == "`":
					i += 2
				case open.open == "`" && jsLike && strings.HasPrefix(s[i:], "${"):
					end := interpEnd(s, i+2)
					if end < 0 {
						i += 2
						continue
					}
					if code == nil {
						code = map[int][][2]int{}
					}
					code[n] = append(code[n], [2]int{i + 2, end})
					i = end + 1
				case strings.HasPrefix(s[i:], open.close):
					i += len(open.close)
					open = nil
				default:
					i++
				}
				continue
			}
			if hash && s[i] == '#' || !hash && strings.HasPrefix(s[i:], "//") {
				break // a line comment
			}
			if s[i] == '\\' {
				i += 2
				continue
			}
			if s[i] == '/' && jsLike && i+1 < len(s) && s[i+1] != '/' && s[i+1] != '*' && regexStart(s[:i]) {
				if end := regexEnd(s, i); end > 0 {
					i = end // a regex literal: its quotes and backticks open nothing
					continue
				}
			}
			found := false
			for k := range marks {
				if strings.HasPrefix(s[i:], marks[k].open) {
					open, i, found = &marks[k], i+len(marks[k].open), true
					break
				}
			}
			if found {
				continue
			}
			if s[i] == '"' || s[i] == '\'' && ext != "rs" {
				// a one-line string: its quotes don't open anything
				q, j := s[i], i+1
				for j < len(s) && s[j] != q {
					if s[j] == '\\' {
						j++
					}
					j++
				}
				i = j + 1
				continue
			}
			i++
		}
	}
	return in, code
}

// interpEnd is the index of the } that closes a ${ whose contents start at
// s[i], or -1 if it doesn't close on this line.
func interpEnd(s string, i int) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}

// anyWithin: a span lies inside one of the ranges.
func anyWithin(spans, ranges [][2]int) bool {
	for _, sp := range spans {
		for _, r := range ranges {
			if sp[0] >= r[0] && sp[1] <= r[1] {
				return true
			}
		}
	}
	return false
}

// docStart is the first line of the comment and decorator block directly
// above line n (n itself if there is none).
func docStart(lines []string, n int, st style, inText map[int]string) int {
	first := n
	for k := n - 1; k >= 1 && k <= len(lines) && n-k <= 200; k-- {
		l := lines[k-1]
		if !commentLine(l, st) && !decorator(l) && inText[k] != "comment" && !strings.HasPrefix(strings.TrimSpace(l), "/*") {
			break
		}
		first = k
	}
	return first
}

// goBlocks marks the lines inside top-level var ( … ) and const ( … )
// blocks of a Go file with "var" or "const".
func goBlocks(path string, lines []string) map[int]string {
	if !strings.HasSuffix(path, ".go") {
		return nil
	}
	var in map[int]string
	kind := ""
	for i, l := range lines {
		switch {
		case kind == "" && (l == "var (" || l == "const ("):
			kind = strings.TrimSuffix(l, " (")
		case kind != "" && strings.HasPrefix(l, ")"):
			kind = ""
		case kind != "":
			if in == nil {
				in = map[int]string{}
			}
			in[i+1] = kind
		}
	}
	return in
}

// regexStart: a slash after this JavaScript code starts a regex literal,
// not a division (it follows an operator, an opening bracket, or nothing).
func regexStart(code string) bool {
	t := strings.TrimRight(code, " \t")
	if t == "" {
		return true
	}
	if strings.ContainsRune("(,=:[!&|?{};+-*%<>~^", rune(t[len(t)-1])) {
		return true
	}
	for _, kw := range []string{"return", "typeof", "case", "in", "of", "delete", "void", "yield", "await"} {
		if strings.HasSuffix(t, kw) && (len(t) == len(kw) || !isWord(t[len(t)-len(kw)-1])) {
			return true
		}
	}
	return false
}

// regexEnd is the index after a regex literal that starts at s[i] ('/'),
// or -1 if it doesn't close on this line.
func regexEnd(s string, i int) int {
	class := false
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '[':
			class = true
		case ']':
			class = false
		case '/':
			if !class {
				return j + 1
			}
		}
	}
	return -1
}

// innermost is the deepest symbol whose lines contain n, or -1.
func innermost(syms []outline.Symbol, n int) int {
	best := -1
	for i, s := range syms {
		if s.Line > n {
			break
		}
		if n <= s.End && (best < 0 || s.Line >= syms[best].Line) {
			best = i
		}
	}
	return best
}

func lastPart(name string) string {
	if i := strings.LastIndexAny(name, ".:"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// how a language writes comments and strings
type style struct {
	line     []string // line comment markers
	block    bool     // /* … */ (and "*" continuation lines)
	quotes   string
	code     bool // source code (not data, docs or prose): keywords define names
	js       bool // JavaScript and TypeScript: /…/ regex literals
	typed    bool // declarations put the type before the name (Java, C#, C, C++)
	fstrings bool // Python f-strings interpolate {…}
}

var (
	slashStyle = style{line: []string{"//"}, block: true, quotes: "\"`", code: true}
	typedStyle = style{line: []string{"//"}, block: true, quotes: "\"", code: true, typed: true}
	jsStyle    = style{line: []string{"//"}, block: true, quotes: "\"'`", code: true, js: true}
	hashStyle  = style{line: []string{"#"}, quotes: "\"'", code: true}
	pyStyle    = style{line: []string{"#"}, quotes: "\"'", code: true, fstrings: true}
	confStyle  = style{line: []string{"#"}, quotes: "\"'"} // YAML, TOML, INI: data
	dashStyle  = style{line: []string{"--"}, quotes: "\"'", code: true}
	cssStyle   = style{block: true, quotes: "\"'"}
	plainStyle = style{} // data, Markdown, HTML: no string labels
)

func styleOf(path string) style {
	base := strings.ToLower(filepath.Base(path))
	switch strings.TrimPrefix(filepath.Ext(base), ".") {
	case "js", "mjs", "cjs", "ts", "tsx", "jsx", "mts", "cts", "svelte", "vue", "php", "dart":
		return jsStyle
	case "c", "h", "cc", "cpp", "cxx", "hpp", "hh", "java", "cs":
		return typedStyle
	case "go", "rs", "kt", "kts", "scala", "swift", "zig", "proto", "glsl", "wgsl", "hlsl", "frag", "vert", "gdshader":
		return slashStyle
	case "py", "pyi":
		return pyStyle
	case "sh", "bash", "zsh", "fish", "rb", "gd", "r", "pl", "mk", "cmake", "nix", "ps1", "tcl", "dockerfile":
		return hashStyle
	case "yaml", "yml", "toml", "conf", "cfg", "ini":
		return confStyle
	case "jsonc":
		return style{line: []string{"//"}, block: true}
	case "lua", "sql", "hs", "elm", "ada":
		return dashStyle
	case "css", "scss", "less":
		return cssStyle
	}
	switch base {
	case "makefile", "dockerfile", "cmakelists.txt", "pkgbuild", ".gitignore", ".env":
		return hashStyle
	}
	return plainStyle
}

var (
	defKeyword  = regexp.MustCompile(`(?:^|[\s(,])(?:function\*?|def|fn|func|class|struct|enum|interface|trait|type|module|namespace|macro_rules!|record|object|union)\s+(?:<[^>]*>\s*)?(?:\([^)]*\)\s*)?[\w.]*$`)
	declKeyword = regexp.MustCompile(`(?:^|[\s(,{])(?:const|let|var|val|local|my|our|static|final|auto|mut|global|nonlocal|readonly)\s+(?:mut\s+|\*\s*|&\s*)*(?:[\w$]+\s*,\s*)*[\[{(]?\s*(?:[\w$]+\s*,\s*)*$`)
	assignAfter = regexp.MustCompile(`^\s*(?:\[[^\]]*\]\s*)?(?:(?:[-+*/%&|^]|<<|>>|>>>|\?\?|\|\||&&|\*\*)?=(?:[^=>~]|$)|\+\+|--)`)
	goDecl      = regexp.MustCompile(`^\s*(?:,\s*[\w$]+\s*)*:=`)
	pyAnnotated = regexp.MustCompile(`^\s*:\s*[\w\[\], .|"']+=(?:[^=]|$)`)
	importLine  = regexp.MustCompile(`^(?:import\b|from\s+\S+\s+import\b|use\s|#include\b|#import\b|require\(|(?:const|let|var)\s+.*=\s*require\(|export\s+\{[^}]*\}\s+from\b|export\s+\*\s+from\b|@import\b|extern\s+crate\b|using\s+[\w.]+;|package\s+[\w.]+;|load\(|preload\()`)
)

// classify labels a hit. With several matches on the line, one in code
// decides (a call after a string is still a call); only if all are in
// comments or strings is the line labelled so.
func classify(h search.Hit, st style) string {
	if len(h.Spans) == 0 {
		return ""
	}
	trimmed := strings.TrimSpace(h.Text)
	first := ""
	for k, sp := range h.Spans {
		before := h.Text[:sp[0]]
		l := ""
		switch {
		case inComment(before, trimmed, st):
			l = "comment"
		case inString(before, st):
			l = "string"
		}
		if l == "" {
			return classifyCode(h, sp, trimmed, st)
		}
		if k == 0 {
			first = l
		}
	}
	return first
}

func classifyCode(h search.Hit, sp [2]int, trimmed string, st style) string {
	before, after := h.Text[:sp[0]], h.Text[sp[1]:]
	if importLine.MatchString(trimmed) {
		return "import"
	}
	if st.code && defKeyword.MatchString(before) {
		return "definition"
	}
	if declKeyword.MatchString(before) || goDecl.MatchString(after) {
		return "declaration"
	}
	if strings.HasPrefix(strings.TrimSpace(after), "==") || strings.HasPrefix(strings.TrimSpace(after), "=>") {
		return ""
	}
	if assignAfter.MatchString(after) || st.line != nil && st.line[0] == "#" && pyAnnotated.MatchString(after) {
		return "assignment"
	}
	return ""
}

// inString: the text ends inside a string literal. Interpolations
// (`${x}` in a template, {x} in a Python f-string) are code, and a
// character literal ('"') opens nothing.
func inString(before string, st style) bool {
	var open byte
	interp := false // the open string interpolates: ` or f"
	depth := 0      // inside ${…} or {…} of an interpolating string
	for i := 0; i < len(before); i++ {
		c := before[i]
		if depth > 0 {
			switch c {
			case '{':
				depth++
			case '}':
				depth--
			}
			continue
		}
		if c == '\\' {
			i++
			continue
		}
		if open != 0 {
			switch {
			case c == open:
				open = 0
			case interp && open == '`' && c == '$' && i+1 < len(before) && before[i+1] == '{':
				depth, i = 1, i+1
			case interp && open != '`' && c == '{':
				if i+1 < len(before) && before[i+1] == '{' {
					i++ // {{ is a literal brace
				} else {
					depth = 1
				}
			}
			continue
		}
		if c == '/' && st.js && i+1 < len(before) && before[i+1] != '/' && before[i+1] != '*' && regexStart(before[:i]) {
			end := regexEnd(before, i)
			if end < 0 {
				return true // the match is inside the regex literal
			}
			i = end - 1
			continue
		}
		if c == '\'' && strings.IndexByte(st.quotes, '\'') < 0 {
			// a character literal: 'x', '\n', '"' (Rust lifetimes have no closing quote)
			if i+2 < len(before) && before[i+2] == '\'' {
				i += 2
			} else if i+3 < len(before) && before[i+1] == '\\' && before[i+3] == '\'' {
				i += 3
			}
			continue
		}
		if strings.IndexByte(st.quotes, c) >= 0 {
			if c == '\'' && i > 0 && isWord(before[i-1]) && !fPrefix(before[:i]) {
				continue // it's, don't
			}
			open = c
			interp = c == '`' || st.fstrings && fPrefix(before[:i])
		}
	}
	return open != 0 && depth == 0
}

// fPrefix: the string about to open is a Python f-string (f"…", rf'…').
func fPrefix(s string) bool {
	n := len(s)
	for k := 1; k <= 2 && k <= n; k++ {
		p := strings.ToLower(s[n-k:])
		if (p == "f" || p == "rf" || p == "fr") && (n == k || !isWord(s[n-k-1])) {
			return true
		}
	}
	return false
}

var (
	typedBefore = regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|final|readonly|const|volatile|transient|abstract|override|virtual|mutable|unsigned|signed|inline|constexpr|required|sealed|partial|new)\s+)*[\w:.<>\[\]?*&, ]*[\w>\]?*&]\s*[*&]?\s*$`)
	typedAfter  = regexp.MustCompile(`^\s*(?:[;=,\[{)]|\(|$|:\s*\d)`)
)

// isField: the hit names a field, attribute, member or (in an interface) a
// method of the type or class directly around it. In Java, C#, C and C++
// the type comes first (private final Sampler sampler;), elsewhere the
// name (Lines int, limit: number, count = 0).
// It returns the field's name, or "".
func isField(s outline.Symbol, h search.Hit, typed bool) string {
	if !containers[s.Kind] || h.Line == s.Line {
		return ""
	}
	for _, sp := range h.Spans { // in "Sampler sampler;" the second match is the name
		a, b := widen(h.Text, sp[0], sp[1])
		if typed && typedBefore.MatchString(h.Text[:a]) && typedAfter.MatchString(h.Text[b:]) ||
			!typed && fieldBefore.MatchString(h.Text[:a]) && fieldAfter.MatchString(h.Text[b:]) {
			return h.Text[a:b]
		}
	}
	return ""
}

// widen grows a match to the whole identifier around it (for --partial and
// regexes: "recent" in recent_projects).
func widen(text string, a, b int) (int, int) {
	for a > 0 && (isWord(text[a-1]) || text[a-1] == '$') {
		a--
	}
	for b < len(text) && (isWord(text[b]) || text[b] == '$') {
		b++
	}
	return a, b
}

// isDef: the hit is on the symbol's first line and matches its name. For
// -d with a regex, any match on that line counts (impl.*Display for).
func isDef(s outline.Symbol, h search.Hit, p *search.Pattern) bool {
	if h.Line != s.Line {
		return false
	}
	if p.DefMode && !p.Smart {
		return true
	}
	names := []string{lastPart(s.Name)}
	if s.Kind == "impl" { // impl Shape for Grid<'a>: defines Shape for Grid
		trait, typ, ok := strings.Cut(s.Name, " for ")
		if !ok {
			trait, typ = s.Name, ""
		}
		names = []string{bare(trait), bare(typ)}
	}
	for _, sp := range h.Spans {
		a, b := sp[0], sp[1]
		if !p.Smart {
			a, b = widen(h.Text, a, b)
		}
		for _, name := range names {
			if name != "" && strings.EqualFold(lastPart(h.Text[a:b]), name) {
				return true
			}
		}
	}
	return false
}

// bare is a type's name without its path and generics: fmt::Display → Display.
func bare(t string) string {
	if i := strings.IndexByte(t, '<'); i >= 0 {
		t = t[:i]
	}
	return lastPart(strings.TrimSpace(t))
}

func inComment(before, trimmed string, st style) bool {
	for _, m := range st.line {
		if strings.HasPrefix(trimmed, m) {
			return true
		}
		// a marker earlier on the line, outside a string
		for i := strings.Index(before, m); i >= 0; {
			if !inString(before[:i], st) && (m != "#" || i == 0 || before[i-1] == ' ' || before[i-1] == '\t') {
				return true
			}
			j := strings.Index(before[i+len(m):], m)
			if j < 0 {
				break
			}
			i += len(m) + j
		}
	}
	if st.block {
		if strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "* ") || trimmed == "*" || strings.HasPrefix(trimmed, "*/") {
			return true
		}
		if i := strings.LastIndex(before, "/*"); i >= 0 && !strings.Contains(before[i:], "*/") && !inString(before[:i], st) {
			return true
		}
	}
	if st.line == nil && !st.block && strings.Contains(before, "<!--") && !strings.Contains(before, "-->") {
		return true
	}
	return false
}

func isWord(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
