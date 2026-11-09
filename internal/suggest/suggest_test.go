package suggest

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Vaspyyy/seek/internal/search"
)

// The reference: the plain way to find similar names, which Similar has
// to agree with. It splits every file with a regular expression, records
// every name with all its files and lines, and compares each with the
// names searched for afterwards.

var refTokenRe = regexp.MustCompile(`[\p{L}_$][\p{L}\p{N}_$]*`)

type refStat struct {
	count int
	paths []string
	first map[string]int
}

// refNames reads the files once: the names in them don't depend on what
// was searched for.
func refNames(paths []string) map[string]*refStat {
	all := map[string]*refStat{}
	for _, p := range paths {
		data, skip := search.Read(p)
		if skip != "" || search.Kind(p, data) != "" {
			continue
		}
		seen := map[string]bool{}
		onLine := map[string]int{}
		line, at := 1, 0
		for _, loc := range refTokenRe.FindAllIndex(data, -1) {
			if loc[1]-loc[0] < 3 {
				continue
			}
			line += bytes.Count(data[at:loc[0]], []byte{'\n'})
			at = loc[0]
			t := string(data[loc[0]:loc[1]])
			if onLine[t] == line {
				continue
			}
			onLine[t] = line
			st := all[t]
			if st == nil {
				st = &refStat{first: map[string]int{}}
				all[t] = st
			}
			st.count++
			if !seen[t] {
				seen[t] = true
				st.paths = append(st.paths, p)
				st.first[p] = line
			}
		}
	}
	for _, st := range all {
		sort.Strings(st.paths)
	}
	return all
}

func refSimilar(all map[string]*refStat, words []string, limit int, fold bool) []Name {
	out := []Name{}
	for tok, st := range all {
		best := 0
		for _, w := range words {
			if s := refScore(lastPart(w), tok); s > 0 && !(fold && s == 1) && (best == 0 || s < best) {
				best = s
			}
		}
		if best > 0 {
			out = append(out, Name{Word: tok, Count: st.count, Paths: st.paths, Line: st.first[st.paths[0]], score: best})
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

func refScore(q, tok string) int {
	if q == tok {
		return 0
	}
	lq, lt := strings.ToLower(q), strings.ToLower(tok)
	switch {
	case lq == lt:
		return 1
	case squash(lq) == squash(lt):
		return 2
	case len(q) >= 5 && refDistance(lq, lt) <= 1, len(q) >= 8 && refDistance(lq, lt) <= 2:
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

func refDistance(a, b string) int {
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

// sameAsRef checks Similar against the reference for each of the names,
// alone and all together, with and without case folding.
func sameAsRef(t *testing.T, paths []string, names []string) {
	t.Helper()
	all := refNames(paths)
	sets := [][]string{names}
	for _, n := range names {
		sets = append(sets, []string{n})
	}
	some := 0
	for _, words := range sets {
		for _, fold := range []bool{false, true} {
			want := refSimilar(all, words, 1<<30, fold)
			some += len(want)
			for _, limit := range []int{1 << 30, 20, 1} {
				got, want := Similar(words, paths, limit, fold), want[:min(len(want), limit)]
				if !reflect.DeepEqual(got, want) {
					t.Errorf("Similar(%q, fold %v, limit %d): %d names, want %d; the first that differ:%s", words, fold, limit, len(got), len(want), firstDiff(got, want))
				}
			}
		}
	}
	if some == 0 {
		t.Error("nothing is similar to any of the names: the comparison checks nothing")
	}
}

func firstDiff(got, want []Name) string {
	for i := 0; i < max(len(got), len(want)); i++ {
		var g, w any = "nothing", "nothing"
		if i < len(got) {
			g = got[i]
		}
		if i < len(want) {
			w = want[i]
		}
		if !reflect.DeepEqual(g, w) {
			return fmt.Sprintf("\n  #%d got  %+v\n  #%d want %+v", i, g, i, w)
		}
	}
	return " none"
}

// The names of seek's own source, and of the folders SEEK_SUGGEST_DIRS
// names (the ones separated by the path list separator), compare the same
// as in the reference.
func TestSimilarAgreesWithTheReferenceOnRealCode(t *testing.T) {
	names := []string{
		"Similr", "lastPrat", "search_file", "SearchFile", "wordlines", "readFileData", "countLine", "Pattern.keepHits",
		"budgt", "render_options", "loadWorldMapData", "save_player_stat", "qzx", "xValue", "getName", "filepath",
		"Options", "isWord", "ChunkPoss", "useEffectEvent", "handle_request", "tokens", "regex", "main",
	}
	for _, root := range append([]string{filepath.Join("..", "..")}, filepath.SplitList(os.Getenv("SEEK_SUGGEST_DIRS"))...) {
		var paths []string
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return nil
			case d.IsDir() && p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "target"):
				return filepath.SkipDir
			case d.Type().IsRegular():
				paths = append(paths, p)
			}
			return nil
		})
		if len(paths) < 10 {
			t.Fatalf("%s: only %d files", root, len(paths))
		}
		sameAsRef(t, paths, names)
	}
}

// Names the byte tables don't cover, lines counted once, names that
// repeat in a file and across files, and more names than a worker
// remembers.
func TestSimilarAgreesWithTheReferenceOnOddText(t *testing.T) {
	defer func(n int) { maxKnown = n }(maxKnown)
	maxKnown = 40
	dir := t.TempDir()
	var many strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&many, "name%05d load_world_%d\n", i, i%7)
		if i%100 == 0 {
			many.WriteString("loadWorldMap LoadWorldMap load_world_map loadWorldMaps\n")
		}
	}
	files := map[string]string{
		"a.go":     "package a\n\nfunc loadWorldMap() {}\nfunc LoadWorldMap() { loadWorldMap(); loadWorldMap() }\n// load_world_map loadworldmap $loadWorldMap _loadWorldMap loadWorldMap2\n",
		"b.py":     "def load_world_map():\n    return loadWorldMaps + laodWorldMap + loadWorldMa\n\n\nx = load_world\ny = world_map_load\n",
		"c.js":     "const größe = 1, Größe = 2, grösse = 3, große_zahl = 4;\nlet день = 1, День = 2, деньги = 3;\nconst 名前 = 1, 名前2 = 2, x名前 = 3;\nfoo٣bar = fooⅧ = 9abc = a·b\n٣loadWorldMaps = Ⅷname00042 = 9loadWorldMaps\n",
		"d.txt":    "loadWorldMap\xff\xfeloadWorldMap \xc3loadWorldMap\xc3\x28 lo\n\nab cd ef\nsaveWorldMap rebuildWorldMap loadCityMap load\n",
		"e.rs":     "fn größe_ändern() {}\nfn GRÖSSE_ÄNDERN() {}\nfn groesse() {}\n",
		"many.txt": many.String(),
		"min.js":   strings.Repeat("loadWorldMapz;", 2000),
		"gen.go":   "// Code generated by x. DO NOT EDIT.\npackage a\nfunc loadWorldMapGen() {}\n",
		"bin.dat":  "loadWorldMapBin",
		"nul.c":    "loadWorldMapNul\x00",
		"empty.go": "",
	}
	var paths []string
	for name, text := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	paths = append(paths, filepath.Join(dir, "gone.go"))
	sort.Strings(paths)
	sameAsRef(t, paths, []string{
		"loadWorldMap", "load_world_map", "LOADWORLDMAP", "loadWorldMpa", "loadMap", "world", "Größe", "größe", "grosse",
		"день", "ДЕНЬ", "名前", "name00042", "name0004", "load_world_3", "a.b.loadWorldMap", "x::load_world_map", "fooⅧ", "größe_ändern",
		"load-world-map", "$loadWorldMap", "lo", "LO", "Ab", "", "Map", "FOOⅧ", "Foo٣Bar", "foo٣baz",
	})
}

// A worker forgets the scores it has when they reach maxKnown, so a
// project with a million names doesn't cost a million entries.
func TestScannerRemembersBoundedScores(t *testing.T) {
	var text strings.Builder
	for i := range 4 * maxKnown {
		fmt.Fprintf(&text, "name%d loadWorldMaps\n", i)
	}
	s := &scanner{qs: []*query{newQuery("loadWorldMap")}, known: map[string]int8{}, found: map[string]*stat{}}
	s.scan("a.txt", []byte(text.String()))
	if len(s.known) > maxKnown {
		t.Errorf("the scanner remembers %d scores, more than maxKnown (%d)", len(s.known), maxKnown)
	}
	if st := s.found["loadWorldMaps"]; len(s.found) != 1 || st == nil || st.count != 4*maxKnown {
		t.Errorf("found %d names, loadWorldMaps: %+v; want it alone, on %d lines", len(s.found), st, 4*maxKnown)
	}
}
