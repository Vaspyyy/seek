// Package search finds the lines of files that match a pattern.
package search

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Pattern is a compiled search pattern.
type Pattern struct {
	Re    *regexp.Regexp
	file  *regexp.Regexp // Re for a whole file: ^ and $ at every line
	Words []string       // the identifiers searched for, if the pattern is a list of them
	Names []string       // identifiers in the pattern, also in a regex (for suggestions)
	Smart bool           // whole words only, because the pattern is a list of identifiers
	Text  bool           // searched as plain text (-F, or not a valid regex)
	Fold  bool           // case-insensitive
	Bad   string         // why the pattern was taken as text, if it wasn't a valid regex
	Expr  string         // what the user typed
	// DefMode: looking for definitions (-d); with a regex, a match anywhere
	// on a definition's first line finds it
	DefMode bool
	need    [][]byte        // a match contains one of these (nil: no such literal)
	words   map[string]bool // many whole words: found by splitting lines into words
}

// Options say how to read a pattern.
type Options struct {
	IgnoreCase bool
	Fixed      bool // plain text, not a regex
	Words      bool // whole words, also for a regex
	Partial    bool // no whole-word matching for identifiers
}

var identRe = regexp.MustCompile(`^\$?[\p{L}_][\p{L}\p{N}_]*(?:(?:\.|::)[\p{L}_][\p{L}\p{N}_]*)*$`)

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
				// \b knows only ASCII letters; boundaries next to other
				// letters are checked on each match (wholeWord)
				if isWordByte(a[0]) {
					q = `\b` + q
				}
				if isWordByte(a[len(a)-1]) {
					q += `\b`
				}
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
	p.file = regexp.MustCompile("(?m)" + src)
	p.Names = p.Words
	if p.Names == nil {
		p.Names = namesIn(expr, o.Fixed || p.Bad != "")
	}
	if len(p.Names) > 6 {
		p.Names = p.Names[:6] // suggestions for the first few are plenty
	}
	if p.Smart && len(p.Words) > 16 && plainWords(p.Words) {
		// a long list of names: one pass per file looking words up in a
		// set, instead of one search per name
		p.words = map[string]bool{}
		for _, w := range p.Words {
			if o.IgnoreCase {
				w = strings.ToLower(w)
			}
			p.words[w] = true
		}
		return p, nil
	}
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

var (
	escapeRe = regexp.MustCompile(`\\.`)
	nameRe   = regexp.MustCompile(`[A-Za-z_$][\w$]{3,}`)
	keywords = map[string]bool{"function": true, "const": true, "return": true, "export": true, "async": true, "await": true,
		"class": true, "self": true, "this": true, "static": true, "import": true, "from": true, "default": true, "public": true,
		"private": true, "struct": true, "impl": true, "type": true, "interface": true, "func": true, "true": true, "false": true,
		"null": true, "None": true, "undefined": true, "else": true, "while": true, "for": true, "new": true}
)

// namesIn finds the identifiers in a regex ("function\s+loadMap|\.mapData"
// has loadMap and mapData), leaving out escapes and common keywords.
func namesIn(expr string, text bool) []string {
	if !text {
		expr = escapeRe.ReplaceAllString(expr, " ")
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range nameRe.FindAllString(expr, -1) {
		if !keywords[n] && !seen[n] && len(out) < 6 {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// plainWords: every word is letters, digits and underscores only, so a
// whole-word match is exactly a word of the line.
func plainWords(ws []string) bool {
	for _, w := range ws {
		for i := 0; i < len(w); i++ {
			if !isWordByte(w[i]) {
				return false
			}
		}
	}
	return true
}

func isWordByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// wordLines finds the lines containing one of the words.
func wordLines(data []byte, words map[string]bool, fold bool) []Hit {
	var hits []Hit
	line := 1
	for start := 0; start < len(data); line++ {
		end := bytes.IndexByte(data[start:], '\n')
		if end < 0 {
			end = len(data) - start
		}
		text := strings.TrimSuffix(string(data[start:start+end]), "\r")
		var spans [][2]int
		for i := 0; i < len(text); {
			if !isWordByte(text[i]) {
				i++
				continue
			}
			j := i
			for j < len(text) && isWordByte(text[j]) {
				j++
			}
			w := text[i:j]
			if fold {
				w = strings.ToLower(w)
			}
			if words[w] {
				spans = append(spans, [2]int{i, j})
			}
			i = j
		}
		if spans != nil {
			hits = append(hits, Hit{Line: line, Text: text, Spans: spans})
		}
		start += end + 1
	}
	return hits
}

// wholeWord: the match isn't part of a longer word made of non-ASCII
// letters (ber_limit inside über_limit), which \b can't see.
func wholeWord(text string, a, b int) bool {
	if a > 0 {
		if r, _ := utf8.DecodeLastRuneInString(text[:a]); wordRune(r) {
			return false
		}
	}
	if b < len(text) {
		if r, _ := utf8.DecodeRuneInString(text[b:]); wordRune(r) {
			return false
		}
	}
	return true
}

func wordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// Keep drops spans that aren't whole words when the pattern wants them.
func (p *Pattern) keep(hits []Hit) []Hit {
	if !p.Smart {
		return hits
	}
	out := hits[:0]
	for _, h := range hits {
		var sp [][2]int
		for _, s := range h.Spans {
			if wholeWord(h.Text, s[0], s[1]) {
				sp = append(sp, s)
			}
		}
		if len(sp) > 0 {
			h.Spans = sp
			out = append(out, h)
		}
	}
	return out
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

var binaryExt = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true, "ico": true, "bmp": true, "tif": true, "tiff": true, "psd": true,
	"avif": true, "heic": true, "mp3": true, "ogg": true, "wav": true, "flac": true, "m4a": true, "opus": true, "mp4": true, "mkv": true,
	"webm": true, "mov": true, "avi": true, "gz": true, "tgz": true, "xz": true, "zst": true, "bz2": true, "zip": true, "7z": true,
	"rar": true, "jar": true, "class": true, "war": true, "pdf": true, "ttf": true, "otf": true, "woff": true, "woff2": true, "eot": true,
	"so": true, "dll": true, "dylib": true, "exe": true, "o": true, "a": true, "lib": true, "obj": true, "pyc": true, "wasm": true,
	"bin": true, "dat": true, "pak": true, "blend": true, "glb": true, "fbx": true, "ktx2": true, "dds": true, "sqlite": true, "db": true,
}

// Read returns a file's contents, or why it isn't searched. Known binary
// types aren't opened, and other files are read only as far as needed to
// see that they're binary.
func Read(path string) ([]byte, string) {
	if binaryExt[strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))] {
		return nil, "binary"
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "unreadable"
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, "unreadable"
	}
	if st.Size() > MaxSize {
		return nil, "too big"
	}
	data := make([]byte, st.Size()+1) // +1: notice a file that grew
	n, err := io.ReadFull(f, data[:min(len(data), 8000)])
	if bytes.IndexByte(data[:n], 0) >= 0 {
		return nil, "binary"
	}
	if err == nil && n < len(data) {
		var m int
		m, err = io.ReadFull(f, data[n:])
		n += m
	}
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, "unreadable"
	}
	return data[:n], ""
}

func searchFile(path string, p *Pattern) (*File, string) {
	data, skip := Read(path)
	if skip != "" {
		return nil, skip
	}
	var hits []Hit
	if p.words != nil {
		hits = wordLines(data, p.words, p.Fold)
	} else if !p.mayMatch(data) {
		return nil, ""
	} else if p.need != nil && shortest(p.need) >= 2 {
		hits = candidateLines(data, p.Re, p.need)
	} else {
		hits = lines(data, p.file, p.Re)
	}
	hits = p.keep(hits)
	if len(hits) == 0 {
		return nil, ""
	}
	return &File{Path: path, Data: data, Lines: CountLines(data), Hits: hits, Kind: Kind(path, data)}, ""
}

// Lines returns the lines of data that match re.
func Lines(data []byte, re *regexp.Regexp) []Hit { return lines(data, re, re) }

// lines finds the candidate lines with fileRe on the whole file (compiled
// with (?m), so ^ and $ match at every line) and matches re on each.
func lines(data []byte, fileRe, re *regexp.Regexp) []Hit {
	locs := fileRe.FindAllIndex(data, -1)
	if n := len(locs); n > 0 && len(data) > 0 && data[len(data)-1] == '\n' && locs[n-1][0] == len(data) {
		locs = locs[:n-1] // after the last line break there is no line
	}
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

// candidateLines matches re only on the lines that contain one of the
// required literals, found with bytes.Index: much faster than running the
// regex over the whole file when it can't skip ahead on its own.
func candidateLines(data []byte, re *regexp.Regexp, need [][]byte) []Hit {
	starts := map[int]bool{}
	for _, lit := range need {
		for pos := 0; pos < len(data); {
			i := bytes.Index(data[pos:], lit)
			if i < 0 {
				break
			}
			at := pos + i
			start := bytes.LastIndexByte(data[:at], '\n') + 1
			starts[start] = true
			end := bytes.IndexByte(data[at:], '\n')
			if end < 0 {
				break
			}
			pos = at + end + 1
		}
	}
	if len(starts) == 0 {
		return nil
	}
	order := make([]int, 0, len(starts))
	for s := range starts {
		order = append(order, s)
	}
	sort.Ints(order)
	var hits []Hit
	line, at := 1, 0
	for _, s := range order {
		line += bytes.Count(data[at:s], []byte{'\n'})
		at = s
		end := bytes.IndexByte(data[s:], '\n')
		if end < 0 {
			end = len(data) - s
		}
		text := strings.TrimSuffix(string(data[s:s+end]), "\r")
		spans := re.FindAllStringIndex(text, -1)
		if len(spans) == 0 {
			continue
		}
		h := Hit{Line: line, Text: text}
		for _, sp := range spans {
			if sp[1] > sp[0] || len(spans) == 1 {
				h.Spans = append(h.Spans, [2]int{sp[0], sp[1]})
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
	case strings.Contains(base, ".min."):
		return "minified"
	case longLines(data):
		switch filepath.Ext(base) {
		case ".js", ".mjs", ".cjs", ".css", ".json", ".html", ".svg", ".map":
			return "minified"
		}
		return "long lines"
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
