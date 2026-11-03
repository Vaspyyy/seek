// Package suggest finds names in the searched files that look like the
// one that wasn't found: other casing, snake_case for camelCase, a typo,
// a longer name with the same parts.
package suggest

import (
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/Vaspyyy/seek/internal/search"
)

// Name is a suggested identifier.
type Name struct {
	Word  string
	Count int      // occurrences
	Paths []string // the files it occurs in
	score int
}

var tokenRe = regexp.MustCompile(`[A-Za-z_$][\w$]*`)

type stat struct {
	count int
	paths []string
}

// Similar returns up to limit names similar to any of words, best first.
// With fold, names that differ only in case aren't similar: they'd have matched.
func Similar(words []string, paths []string, limit int, fold bool) []Name {
	counts := tokens(paths)
	var out []Name
	for tok, st := range counts {
		best := 0
		for _, w := range words {
			if s := score(lastPart(w), tok); s > 0 && !(fold && s == 1) && (best == 0 || s < best) {
				best = s
			}
		}
		if best > 0 {
			out = append(out, Name{Word: tok, Count: st.count, Paths: st.paths, score: best})
		}
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

// tokens counts the identifiers in paths and where they occur.
func tokens(paths []string) map[string]*stat {
	var mu sync.Mutex
	all := map[string]*stat{}
	jobs := make(chan string)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := map[string]*stat{}
			for p := range jobs {
				data, skip := search.Read(p)
				if skip != "" || search.Kind(p, data) != "" {
					continue
				}
				seen := map[string]bool{}
				for _, m := range tokenRe.FindAll(data, -1) {
					if len(m) < 3 {
						continue
					}
					t := string(m)
					st := local[t]
					if st == nil {
						st = &stat{}
						local[t] = st
					}
					st.count++
					if !seen[t] {
						seen[t] = true
						st.paths = append(st.paths, p)
					}
				}
			}
			mu.Lock()
			for t, st := range local {
				a := all[t]
				if a == nil {
					all[t] = st
					continue
				}
				a.count += st.count
				a.paths = append(a.paths, st.paths...)
			}
			mu.Unlock()
		}()
	}
	for _, p := range paths {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	for _, st := range all {
		sort.Strings(st.paths)
	}
	return all
}

func lastPart(name string) string {
	if i := strings.LastIndexAny(name, ".:"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// score is how alike q and tok are (1 best), or 0 if not alike.
func score(q, tok string) int {
	if q == tok {
		return 0
	}
	lq, lt := strings.ToLower(q), strings.ToLower(tok)
	switch {
	case lq == lt:
		return 1
	case squash(lq) == squash(lt):
		return 2
	case len(q) >= 5 && distance(lq, lt) <= 1, len(q) >= 8 && distance(lq, lt) <= 2:
		return 3
	case len(parts(q)) > 1 && inOrder(parts(q), parts(tok)):
		return 4
	case len(q) >= 4 && strings.Contains(lt, lq) && len(tok) <= 3*len(q):
		return 5
	case len(tok) >= 5 && strings.Contains(lq, lt) && 5*len(tok) >= 3*len(q):
		return 6
	case renamed(parts(q), parts(tok)):
		return 7
	case sameVerb(parts(q), parts(tok)):
		return 8
	}
	return 0
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
// swaps of neighbours).
func distance(a, b string) int {
	if d := len(a) - len(b); d > 2 || d < -2 {
		return 3
	}
	ra, rb := []rune(a), []rune(b)
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(rb)]
}
