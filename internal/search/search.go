// Package search finds the lines of files that match a pattern.
package search

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"runtime"
	"strings"
	"sync"
)

// Pattern is a compiled search pattern.
type Pattern struct {
	Re    *regexp.Regexp
	Words []string // the identifiers searched for, if the pattern is a list of them
	Smart bool     // whole words only, because the pattern is a list of identifiers
	Text  bool     // searched as plain text (-F, or not a valid regex)
	Fold  bool     // case-insensitive
	Bad   string   // why the pattern was taken as text, if it wasn't a valid regex
	Expr  string   // what the user typed
	need  [][]byte // a match contains one of these (nil: no such literal)
}

// Options say how to read a pattern.
type Options struct {
	IgnoreCase bool
	Fixed      bool // plain text, not a regex
	Words      bool // whole words, also for a regex
	Partial    bool // no whole-word matching for identifiers
}

var identRe = regexp.MustCompile(`^\$?[A-Za-z_][\w]*(?:(?:\.|::)[A-Za-z_][\w]*)*$`)

// Compile reads expr. A pattern that is one identifier or several joined
// by | ("setSpeed", "Repo.Open", "a|b|c") matches whole words only, unless
// Partial is set.
func Compile(expr string, o Options) (*Pattern, error) {
	p := &Pattern{Expr: expr, Fold: o.IgnoreCase, Text: o.Fixed}
	alts := strings.Split(expr, "|")
	ident := expr != ""
	for _, a := range alts {
		if !identRe.MatchString(a) {
			ident = false
		}
	}
	if o.Fixed {
		ident = identRe.MatchString(expr)
		alts = []string{expr}
	}
	var src string
	switch {
	case ident:
		p.Words = alts
		var parts []string
		for _, a := range alts {
			q := regexp.QuoteMeta(a)
			if !o.Partial {
				if !strings.HasPrefix(a, "$") {
					q = `\b` + q
				}
				q += `\b`
			}
			parts = append(parts, q)
		}
		p.Smart = !o.Partial
		src = strings.Join(parts, "|")
	case o.Fixed:
		src = regexp.QuoteMeta(expr)
	default:
		src = expr
		if _, err := regexp.Compile(src); err != nil {
			p.Bad = shortErr(err)
			p.Text = true
			src = regexp.QuoteMeta(expr)
		}
	}
	if o.Words && !p.Smart {
		src = `\b(?:` + src + `)\b`
	}
	if o.IgnoreCase {
		src = "(?i)" + src
	}
	re, err := regexp.Compile(src)
	if err != nil {
		return nil, err
	}
	p.Re = re
	if !o.IgnoreCase {
		if sre, err := syntax.Parse(src, syntax.Perl); err == nil {
			p.need = required(sre.Simplify())
		}
	}
	return p, nil
}

// required returns literals one of which every match contains, or nil if
// there are none worth checking. Files without any are skipped unread by
// the regex engine, which is slow on patterns that start with \b.
func required(re *syntax.Regexp) [][]byte {
	switch re.Op {
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase != 0 {
			return nil
		}
		return [][]byte{[]byte(string(re.Rune))}
	case syntax.OpCapture, syntax.OpPlus:
		return required(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return required(re.Sub[0])
		}
	case syntax.OpConcat:
		var best [][]byte
		for _, s := range re.Sub {
			if r := required(s); r != nil && (best == nil || shortest(r) > shortest(best)) {
				best = r
			}
		}
		return best
	case syntax.OpAlternate:
		var all [][]byte
		for _, s := range re.Sub {
			r := required(s)
			if r == nil {
				return nil
			}
			all = append(all, r...)
		}
		return all
	}
	return nil
}

func shortest(lits [][]byte) int {
	n := -1
	for _, l := range lits {
		if n < 0 || len(l) < n {
			n = len(l)
		}
	}
	return n
}

// mayMatch: data contains one of the required literals (or there are none).
func (p *Pattern) mayMatch(data []byte) bool {
	if p.need == nil || shortest(p.need) < 2 {
		return true
	}
	for _, l := range p.need {
		if bytes.Contains(data, l) {
			return true
		}
	}
	return false
}

func shortErr(err error) string {
	s := err.Error()
	s = strings.TrimPrefix(s, "error parsing regexp: ")
	return s
}

// Hit is a line with one or more matches.
type Hit struct {
	Line  int      // 1-based
	Spans [][2]int // byte ranges of the matches in Text
	Text  string   // the line, without its line break
}

// File is a file with hits.
type File struct {
	Path  string
	Data  []byte
	Lines int
	Hits  []Hit
	Kind  string // "lock file", "generated", "minified", "vendored" or ""
}

// MaxSize is the largest file searched.
const MaxSize = 32 << 20

// Stats count what a search looked at.
type Stats struct {
	Files, Binary, TooBig int
}

// Run searches paths in parallel and returns the files with hits, in the
// order of paths.
func Run(paths []string, p *Pattern) ([]*File, Stats) {
	results := make([]*File, len(paths))
	var st Stats
	var mu sync.Mutex
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < runtime.GOMAXPROCS(0); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local Stats
			for i := range jobs {
				f, skip := searchFile(paths[i], p)
				switch skip {
				case "binary":
					local.Binary++
				case "too big":
					local.TooBig++
				default:
					local.Files++
				}
				results[i] = f
			}
			mu.Lock()
			st.Files += local.Files
			st.Binary += local.Binary
			st.TooBig += local.TooBig
			mu.Unlock()
		}()
	}
	for i := range paths {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	var out []*File
	for _, f := range results {
		if f != nil {
			out = append(out, f)
		}
	}
	return out, st
}

// Read returns a file's contents, or why it isn't searched.
func Read(path string) ([]byte, string) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, "unreadable"
	}
	if st.Size() > MaxSize {
		return nil, "too big"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "unreadable"
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return nil, "binary"
	}
	return data, ""
}

func searchFile(path string, p *Pattern) (*File, string) {
	data, skip := Read(path)
	if skip != "" {
		return nil, skip
	}
	if !p.mayMatch(data) {
		return nil, ""
	}
	hits := Lines(data, p.Re)
	if len(hits) == 0 {
		return nil, ""
	}
	return &File{Path: path, Data: data, Lines: CountLines(data), Hits: hits, Kind: Kind(path, data)}, ""
}

// Lines returns the lines of data that match re.
func Lines(data []byte, re *regexp.Regexp) []Hit {
	locs := re.FindAllIndex(data, -1)
	if len(locs) == 0 {
		return nil
	}
	// the whole-file search finds the candidate lines; each is then matched
	// on its own, so matches never span lines and ^ and $ mean its ends
	var hits []Hit
	line, lineStart, pos := 1, 0, 0
	lastLine := 0
	for _, l := range locs {
		for pos < l[0] {
			nl := bytes.IndexByte(data[pos:l[0]], '\n')
			if nl < 0 {
				break
			}
			pos += nl + 1
			line++
			lineStart = pos
		}
		pos = l[0]
		if line == lastLine {
			continue
		}
		lastLine = line
		end := bytes.IndexByte(data[lineStart:], '\n')
		if end < 0 {
			end = len(data) - lineStart
		}
		text := strings.TrimSuffix(string(data[lineStart:lineStart+end]), "\r")
		spans := re.FindAllStringIndex(text, -1)
		if len(spans) == 0 {
			continue
		}
		h := Hit{Line: line, Text: text}
		for _, s := range spans {
			if s[1] > s[0] || len(spans) == 1 {
				h.Spans = append(h.Spans, [2]int{s[0], s[1]})
			}
		}
		hits = append(hits, h)
	}
	return hits
}

// CountLines counts lines, a last one without a line break included.
func CountLines(data []byte) int {
	n := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

var lockNames = map[string]bool{
	"go.sum": true, "cargo.lock": true, "package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"poetry.lock": true, "uv.lock": true, "composer.lock": true, "gemfile.lock": true, "pipfile.lock": true,
	"bun.lock": true, "flake.lock": true, "mix.lock": true, "pubspec.lock": true, "podfile.lock": true,
}

// Kind tells files whose hits matter less: lock files, generated and
// minified code, vendored dependencies.
func Kind(path string, data []byte) string {
	p := filepath.ToSlash(strings.ToLower(path))
	base := p[strings.LastIndexByte(p, '/')+1:]
	switch {
	case lockNames[base]:
		return "lock file"
	case strings.Contains("/"+p, "/vendor/") || strings.Contains("/"+p, "/node_modules/") ||
		strings.Contains("/"+p, "/third_party/") || strings.Contains("/"+p, "/thirdparty/"):
		return "vendored"
	case strings.Contains(base, ".min.") || longLines(data):
		return "minified"
	case generated(data):
		return "generated"
	}
	return ""
}

func longLines(data []byte) bool {
	lines := CountLines(data)
	return len(data) > 5000 && lines > 0 && len(data)/lines > 500
}

func generated(data []byte) bool {
	head := data[:min(len(data), 2000)]
	for _, m := range []string{"Code generated", "DO NOT EDIT", "@generated", "<auto-generated", "autogenerated by"} {
		if bytes.Contains(head, []byte(m)) {
			return true
		}
	}
	return false
}
