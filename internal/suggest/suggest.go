// Package suggest finds names in the searched files that look like the
// one that wasn't found: other casing, snake_case for camelCase, a typo,
// a longer name with the same parts.
package suggest

import (
	"bytes"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Vaspyyy/seek/internal/search"
)

// Name is a suggested identifier.
type Name struct {
	Word  string
	Count int      // occurrences
	Paths []string // the files it occurs in
	Line  int      // its first line in Paths[0]
	score int
}

// stat is what's known about a name that is alike.
type stat struct {
	score int
	count int            // the lines it's on
	paths []string       // the files it's in
	first map[string]int // the first line it's on, per file
	file  int            // the last file and line it was counted on (per worker)
	line  int
}

// Similar returns up to limit names similar to any of words, best first.
// With fold, names that differ only in case aren't similar: they'd have matched.
func Similar(words []string, paths []string, limit int, fold bool) []Name {
	if len(words) == 0 {
		return nil
	}
	qs := make([]*query, len(words))
	for i, w := range words {
		qs[i] = newQuery(lastPart(w))
	}
	found := alike(paths, qs, fold)
	out := make([]Name, 0, len(found))
	for tok, st := range found {
		sort.Strings(st.paths)
		out = append(out, Name{Word: tok, Count: st.count, Paths: st.paths, Line: st.first[st.paths[0]], score: st.score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score < out[j].score
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Word < out[j].Word
	})
	return out[:min(len(out), limit)]
}

// Score is how alike the name is to the one searched for (lower is closer).
func (n Name) Score() int { return n.score }

// alike reads the files and returns the identifiers in them that are like
// one of the names searched for, with where they occur. It runs after a
// search that found nothing, over every file again, so it keeps only what
// it needs: a name is compared once per worker, and nothing is recorded
// about the names that aren't alike, which are nearly all of them.
func alike(paths []string, qs []*query, fold bool) map[string]*stat {
	var mu sync.Mutex
	all := map[string]*stat{}
	jobs := make(chan string)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &scanner{qs: qs, fold: fold, known: map[string]int8{}, found: map[string]*stat{}}
			for p := range jobs {
				data, skip := search.Read(p)
				if skip != "" || search.Kind(p, data) != "" {
					continue
				}
				s.scan(p, data)
			}
			mu.Lock()
			for t, st := range s.found {
				a := all[t]
				if a == nil {
					all[t] = st
					continue
				}
				a.count += st.count
				a.paths = append(a.paths, st.paths...)
				for p, l := range st.first {
					a.first[p] = l
				}
			}
			mu.Unlock()
		}()
	}
	for _, p := range paths {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	return all
}

// maxKnown is how many names a worker remembers the score of. A project
// has far more names than that, but a file mostly repeats a few. (A
// variable for the tests.)
var maxKnown = 1 << 15

// scanner finds the names that are alike in one worker's files.
type scanner struct {
	qs    []*query
	fold  bool
	known map[string]int8 // the score of the names compared so far (0: not alike)
	found map[string]*stat
	files int // files scanned

	// scratch space for score
	lower []byte
	runes []rune
	rows  [3][]int
}

// An identifier is [\p{L}_$][\p{L}\p{N}_$]*. These say which ASCII bytes
// start and continue one.
var startByte, contByte [utf8.RuneSelf]bool

func init() {
	for c := range startByte {
		startByte[c] = 'a' <= c|0x20 && c|0x20 <= 'z' || c == '_' || c == '$'
		contByte[c] = startByte[c] || '0' <= c && c <= '9'
	}
}

// scan counts the names in data that are alike: a line counts once, as in
// the search.
func (s *scanner) scan(path string, data []byte) {
	s.files++
	line, at := 1, 0 // the line of data[at]
	for i := 0; i < len(data); {
		if c := data[i]; c < utf8.RuneSelf {
			if !startByte[c] {
				i++
				continue
			}
		} else if r, n := utf8.DecodeRune(data[i:]); !unicode.IsLetter(r) {
			i += n
			continue
		}
		a := i
		for i < len(data) {
			if c := data[i]; c < utf8.RuneSelf {
				if !contByte[c] {
					break
				}
				i++
			} else if r, n := utf8.DecodeRune(data[i:]); unicode.IsLetter(r) || unicode.IsNumber(r) {
				i += n
			} else {
				break
			}
		}
		if i-a < 3 {
			continue
		}
		tok := data[a:i]
		sc, ok := s.known[string(tok)]
		if !ok {
			sc = int8(s.best(tok))
			if len(s.known) >= maxKnown {
				clear(s.known)
			}
			s.known[string(tok)] = sc
		}
		if sc == 0 {
			continue
		}
		line += bytes.Count(data[at:a], []byte{'\n'})
		at = a
		st := s.found[string(tok)]
		if st == nil {
			st = &stat{score: int(sc), first: map[string]int{}}
			s.found[string(tok)] = st
		}
		if st.file == s.files && st.line == line {
			continue
		}
		if st.file != s.files {
			st.paths = append(st.paths, path)
			st.first[path] = line
		}
		st.file, st.line = s.files, line
		st.count++
	}
}

// best is the closest tok comes to one of the names searched for.
func (s *scanner) best(tok []byte) int {
	best := 0
	for _, q := range s.qs {
		if sc := s.score(q, tok); sc > 0 && !(s.fold && sc == 1) && (best == 0 || sc < best) {
			best = sc
		}
	}
	return best
}

// query is a name that was searched for, with what score needs of it.
type query struct {
	name     string
	lower    []byte
	runes    []rune // of lower
	squashed string
	parts    []string
	first    []byte // the first and the last of several parts
	last     []byte
}

func newQuery(name string) *query {
	lower := strings.ToLower(name)
	q := &query{name: name, lower: []byte(lower), runes: []rune(lower), squashed: squash(lower), parts: parts(name)}
	if len(q.parts) > 1 {
		q.first, q.last = []byte(q.parts[0]), []byte(q.parts[len(q.parts)-1])
	}
	return q
}

func lastPart(name string) string {
	if i := strings.LastIndexAny(name, ".:"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// score is how alike tok is to q (1 best), or 0 if not alike. It's called
// for every name in the project, and nearly all of them aren't: those are
// turned down without allocating.
func (s *scanner) score(q *query, tok []byte) int {
	if string(tok) == q.name {
		return 0
	}
	lt := s.lowered(tok)
	n := len(q.name)
	switch {
	case bytes.Equal(lt, q.lower):
		return 1
	case squashedIs(lt, q.squashed):
		return 2
	}
	if d := len(q.lower) - len(lt); n >= 5 && -2 <= d && d <= 2 {
		if d := s.distance(q.runes, lt); d <= 1 || n >= 8 && d <= 2 {
			return 3
		}
	}
	// The rules about parts need tok to have q's first or last part, so
	// it's split only then.
	var tp []string
	multi := len(q.parts) > 1 && (bytes.Contains(lt, q.first) || bytes.Contains(lt, q.last))
	if multi {
		tp = parts(string(tok))
		if inOrder(q.parts, tp) {
			return 4
		}
	}
	switch {
	case n >= 4 && len(tok) <= 3*n && bytes.Contains(lt, q.lower):
		return 5
	case len(tok) >= 5 && 5*len(tok) >= 3*n && bytes.Contains(q.lower, lt):
		return 6
	case multi && renamed(q.parts, tp):
		return 7
	case multi && sameVerb(q.parts, tp):
		return 8
	}
	return 0
}

// lowered is tok in lower case, in the scanner's scratch space.
func (s *scanner) lowered(tok []byte) []byte {
	s.lower = s.lower[:0]
	for _, c := range tok {
		if c >= utf8.RuneSelf {
			s.lower = append(s.lower[:0], strings.ToLower(string(tok))...)
			break
		}
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		s.lower = append(s.lower, c)
	}
	return s.lower
}

// squashedIs reports whether s without its separators is want.
func squashedIs(s []byte, want string) bool {
	k := 0
	for _, c := range s {
		if c == '_' || c == '-' || c == '$' {
			continue
		}
		if k == len(want) || want[k] != c {
			return false
		}
		k++
	}
	return k == len(want)
}

// sameVerb: both names have several parts and start with the same one
// (save_project and save_settings).
func sameVerb(q, t []string) bool {
	return len(q) >= 2 && len(t) >= 2 && q[0] == t[0] && len(q[0]) >= 3
}

// renamed: the names differ in one part, not the last (rebuildFrontlineField
// and invalidateFrontlineField, save_project and load_project).
func renamed(q, t []string) bool {
	if len(q) < 2 || len(t) < 2 || q[len(q)-1] != t[len(t)-1] {
		return false
	}
	shared := 0
	for _, p := range q {
		for _, x := range t {
			if p == x {
				shared++
				break
			}
		}
	}
	return shared >= len(q)-1 && shared >= len(t)-1
}

func squash(s string) string {
	return strings.NewReplacer("_", "", "-", "", "$", "").Replace(s)
}

// parts splits camelCase and snake_case names: loadWorldMap → load world map.
func parts(s string) []string {
	var out []string
	var cur []rune
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r == '_' || r == '$' || r == '-':
			if len(cur) > 0 {
				out = append(out, strings.ToLower(string(cur)))
				cur = nil
			}
			continue
		case unicode.IsUpper(r) && len(cur) > 0 && (unicode.IsLower(cur[len(cur)-1]) || i+1 < len(rs) && unicode.IsLower(rs[i+1])):
			out = append(out, strings.ToLower(string(cur)))
			cur = nil
		}
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		out = append(out, strings.ToLower(string(cur)))
	}
	return out
}

// inOrder: every part of q appears in t, in the same order.
func inOrder(q, t []string) bool {
	if len(t) <= len(q) {
		return false
	}
	k := 0
	for _, p := range t {
		if k < len(q) && (p == q[k] || len(q[k]) >= 3 && strings.HasPrefix(p, q[k])) {
			k++
		}
	}
	return k == len(q)
}

// distance is the edit distance (insertions, deletions, substitutions and
// swaps of neighbours) between a and b.
func (s *scanner) distance(a []rune, b []byte) int {
	rb := s.runes[:0]
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		rb = append(rb, r)
		b = b[n:]
	}
	s.runes = rb
	for i := range s.rows {
		if cap(s.rows[i]) <= len(rb) {
			s.rows[i] = make([]int, len(rb)+1, 2*len(rb)+1)
		}
		s.rows[i] = s.rows[i][:len(rb)+1]
	}
	prev2, prev, cur := s.rows[0], s.rows[1], s.rows[2] // what's left in them from last time isn't read
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if a[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == rb[j-2] && a[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(rb)]
}
