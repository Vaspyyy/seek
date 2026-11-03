// Package group puts a file's hits under the definitions that contain them
// and labels each hit: definition, declaration, assignment, import,
// comment or string.
package group

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Vaspyyy/glint/outline"
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
	Sig       string
	Line, End int
	Hits      []Hit
}

// File is a searched file with its hits grouped.
type File struct {
	*search.File
	Syms     []outline.Symbol
	Outlined bool
	Groups   []*Group // in line order; the top-level group first if there is one
	Defs     []Def    // definitions among the hits
}

// Def is a definition of what was searched for.
type Def struct {
	Kind, Name, Sig string
	Line, End       int
	Outline         bool // found by the outline rules (not only by a keyword on its line)
}

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

// Build groups f's hits.
func Build(f *search.File, p *search.Pattern) *File {
	out := &File{File: f}
	if outline.Supported(f.Path) && f.Kind != "minified" {
		out.Syms = outline.Parse(f.Path, f.Data, 1<<20)
		out.Outlined = true
	}
	st := styleOf(f.Path)
	byKey := map[int]*Group{}
	var lines []string
	for _, h := range f.Hits {
		si := innermost(out.Syms, h.Line)
		if len(out.Syms) > 0 && commentLine(h.Text, st) {
			if lines == nil {
				lines = strings.Split(string(f.Data), "\n")
			}
			if d := docOf(out.Syms, si, h.Line, lines, st); d >= 0 {
				si = d // a doc comment belongs to the definition below it
			}
		}
		g := byKey[si]
		if g == nil {
			g = &Group{Sym: si}
			if si >= 0 {
				s := out.Syms[si]
				g.Kind, g.Name, g.Sig, g.Line, g.End = s.Kind, outline.Qualified(out.Syms, si), s.Sig, s.Line, s.End
			}
			byKey[si] = g
			out.Groups = append(out.Groups, g)
		}
		hit := Hit{Hit: h}
		if si >= 0 && isDef(out.Syms[si], h, p) {
			hit.Label = "definition"
			s := out.Syms[si]
			out.Defs = append(out.Defs, Def{Kind: s.Kind, Name: g.Name, Sig: s.Sig, Line: s.Line, End: s.End, Outline: true})
		} else {
			hit.Label = classify(h, st)
			if hit.Label == "definition" {
				out.Defs = append(out.Defs, Def{Kind: "def", Name: h.Text[h.Spans[0][0]:h.Spans[0][1]], Sig: clip(strings.TrimSpace(h.Text), 110), Line: h.Line, End: h.Line})
			}
		}
		g.Hits = append(g.Hits, hit)
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

// docOf is the definition that the comment on line n documents: the next
// one, if only comment and decorator lines lie between them, and it's
// inside the same definition as the comment. -1 if there's none.
func docOf(syms []outline.Symbol, in, n int, lines []string, st style) int {
	for i, s := range syms {
		if s.Line <= n {
			continue
		}
		if s.Parent != in {
			return -1
		}
		for k := n + 1; k < s.Line; k++ {
			if k > len(lines) || !commentLine(lines[k-1], st) && !decorator(lines[k-1]) {
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

// isDef: the hit is on the symbol's first line and matches its name.
func isDef(s outline.Symbol, h search.Hit, p *search.Pattern) bool {
	if h.Line != s.Line {
		return false
	}
	name := lastPart(s.Name)
	for _, sp := range h.Spans {
		m := h.Text[sp[0]:sp[1]]
		mm := lastPart(m)
		if strings.EqualFold(mm, name) || !p.Smart && len(m) >= 3 && strings.Contains(strings.ToLower(name), strings.ToLower(m)) {
			return true
		}
		if !p.Smart && strings.Contains(m, name) {
			return true
		}
	}
	return false
}

// how a language writes comments and strings
type style struct {
	line   []string // line comment markers
	block  bool     // /* … */ (and "*" continuation lines)
	quotes string
}

var (
	slashStyle = style{line: []string{"//"}, block: true, quotes: "\"`"}
	jsStyle    = style{line: []string{"//"}, block: true, quotes: "\"'`"}
	hashStyle  = style{line: []string{"#"}, quotes: "\"'"}
	dashStyle  = style{line: []string{"--"}, quotes: "\"'"}
	cssStyle   = style{block: true, quotes: "\"'"}
	plainStyle = style{} // data, Markdown, HTML: no string labels
)

func styleOf(path string) style {
	base := strings.ToLower(filepath.Base(path))
	switch strings.TrimPrefix(filepath.Ext(base), ".") {
	case "js", "mjs", "cjs", "ts", "tsx", "jsx", "mts", "cts", "svelte", "vue", "php", "dart":
		return jsStyle
	case "go", "rs", "c", "h", "cc", "cpp", "cxx", "hpp", "hh", "java", "kt", "kts", "cs", "scala", "swift", "zig", "proto", "glsl", "wgsl", "hlsl", "frag", "vert", "gdshader", "jsonc":
		return slashStyle
	case "py", "pyi", "sh", "bash", "zsh", "fish", "rb", "yaml", "yml", "toml", "gd", "r", "pl", "conf", "cfg", "ini", "mk", "cmake", "nix", "ps1", "tcl", "dockerfile":
		return hashStyle
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
	defKeyword  = regexp.MustCompile(`(?:^|[\s(,])(?:function\*?|def|fn|func|class|struct|enum|interface|trait|type|module|namespace|macro_rules!|record|object|union|impl)\s+(?:<[^>]*>\s*)?(?:\([^)]*\)\s*)?[\w.]*$`)
	declKeyword = regexp.MustCompile(`(?:^|[\s(,{])(?:const|let|var|val|local|my|our|static|final|auto|mut|global|nonlocal|readonly)\s+(?:mut\s+|\*\s*|&\s*)*(?:[\w$]+\s*,\s*)*[\[{(]?\s*(?:[\w$]+\s*,\s*)*$`)
	assignAfter = regexp.MustCompile(`^\s*(?:\[[^\]]*\]\s*)?(?:(?:[-+*/%&|^]|<<|>>|>>>|\?\?|\|\||&&|\*\*)?=(?:[^=>~]|$)|\+\+|--)`)
	goDecl      = regexp.MustCompile(`^\s*(?:,\s*[\w$]+\s*)*:=`)
	pyAnnotated = regexp.MustCompile(`^\s*:\s*[\w\[\], .|"']+=(?:[^=]|$)`)
	importLine  = regexp.MustCompile(`^(?:import\b|from\s+\S+\s+import\b|use\s|#include\b|#import\b|require\(|(?:const|let|var)\s+.*=\s*require\(|export\s+\{[^}]*\}\s+from\b|export\s+\*\s+from\b|@import\b|extern\s+crate\b|using\s+[\w.]+;|package\s+[\w.]+;|load\(|preload\()`)
)

// classify labels a hit by its first match.
func classify(h search.Hit, st style) string {
	if len(h.Spans) == 0 {
		return ""
	}
	start, end := h.Spans[0][0], h.Spans[0][1]
	before, after := h.Text[:start], h.Text[end:]
	trimmed := strings.TrimSpace(h.Text)
	if inComment(before, trimmed, st) {
		return "comment"
	}
	if inString(before, st) {
		return "string"
	}
	if importLine.MatchString(trimmed) {
		return "import"
	}
	if defKeyword.MatchString(before) {
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

// inString: an odd number of unescaped quotes of one kind precede the match.
func inString(before string, st style) bool {
	var open byte
	for i := 0; i < len(before); i++ {
		c := before[i]
		if c == '\\' {
			i++
			continue
		}
		if open != 0 {
			if c == open {
				open = 0
			}
			continue
		}
		if strings.IndexByte(st.quotes, c) >= 0 {
			if c == '\'' && i > 0 && isWord(before[i-1]) {
				continue // it's, don't
			}
			open = c
		}
	}
	return open != 0
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
