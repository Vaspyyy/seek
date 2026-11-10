package suggest

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/Vaspyyy/seek/internal/search"
)

// The reference: the plain way to find similar names, which Similar has
// to agree with. It splits every file with a regular expression, records
// every name with all its files and lines, and compares each with the
// names searched for afterwards. It shares the rules about a name's parts
// (parts, inOrder, renamed, sameVerb, squash, lastPart) with Similar:
// TestScoreExamples says what those give.

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
	var out []Name
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
// alone and all together, with and without case folding. With workers, it
// does so with that many of them at a time: one worker has nothing to
// merge, several do.
func sameAsRef(t *testing.T, paths []string, names []string, workers ...int) {
	t.Helper()
	if len(workers) == 0 {
		workers = []int{runtime.GOMAXPROCS(0)}
	}
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	all := refNames(paths)
	sets := [][]string{names}
	for _, n := range names {
		sets = append(sets, []string{n})
	}
	some := 0
	for i, words := range sets {
		limits := []int{1 << 30}
		if i == 0 {
			limits = append(limits, 20, 1, 0)
		}
		for _, fold := range []bool{false, true} {
			want := refSimilar(all, words, 1<<30, fold)
			some += len(want)
			for k, n := range workers {
				if i > 0 && k != i%len(workers) {
					continue // a name alone: with one of the worker counts, in turn
				}
				runtime.GOMAXPROCS(n)
				for _, limit := range limits {
					got, want := Similar(words, paths, limit, fold), want[:min(len(want), limit)]
					if !reflect.DeepEqual(got, want) {
						t.Errorf("Similar(%q, fold %v, limit %d) with %d workers: %d names, want %d; the first that differ:%s", words, fold, limit, n, len(got), len(want), firstDiff(got, want))
					}
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
// repeat in a file and across files, more names than a worker remembers,
// names at the end of a file, lines that end in \r\n, letters whose
// lower case has another length, a path given twice and paths out of
// order.
func TestSimilarAgreesWithTheReferenceOnOddText(t *testing.T) {
	defer func(n int) { maxKnown = n }(maxKnown)
	maxKnown = 40
	dir := t.TempDir()
	var many strings.Builder
	for i := range 500 {
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
		"crlf.txt": "loadWorldMaps\r\nloadWorldMaps\rloadWorldMaps\r\n\rload_world_maps\n",
		"end.txt":  "x = loadWorldMaps",
		"half.txt": "x = loadWorldMaps\xe5\x90",
		"late.txt": strings.Repeat("x = 1\n", 1400) + "\x00\nloadWorldMaps\x00loadWorldMapLate\n",
		"runes.txt": "café Café cafe\u0301 = 1\n𝐀𝐁𝐂x 𝐀𝐁𝐂 = 1\nǅungla ǆungla = 1\narea² Area² ²area = 1\nname\ufffd = 1\n" +
			"İİİİab abcdİİİİİ İİİ = 1\n\u212aelvin_scale kelvinScale\nΣΟΦΟΣ σοφος σοφοσ\nGROẞE große\n" +
			"map mapper bitmap дь éa 名 = 1\nloadCityMap loadCityMaps loadCityMapsX\n___ $$$ _$_ abc\n",
	}
	var paths []string
	for name, text := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	paths = append(paths, filepath.Join(dir, "gone.go"), filepath.Join(dir, "a.go"), filepath.Join(dir, "runes.txt"))
	sort.Strings(paths)
	slices.Reverse(paths)
	sameAsRef(t, paths, []string{
		"loadWorldMap", "load_world_map", "LOADWORLDMAP", "loadWorldMpa", "loadMap", "world", "Größe", "größe", "grosse",
		"день", "ДЕНЬ", "名前", "name00042", "name0004", "load_world_3", "a.b.loadWorldMap", "x::load_world_map", "fooⅧ", "größe_ändern",
		"load-world-map", "$loadWorldMap", "lo", "LO", "Ab", "", "Map", "FOOⅧ", "Foo٣Bar", "foo٣baz",
		"ДЬ", "Cafe", "𝐀𝐁𝐂", "ǆungla", "Area²", "NAME", "iiiiabc", "abcd", "xiiix", "kelvin_scale", "KelvinScale",
		"σοφος", "ΣΟΦΟΣ_", "a.", "x:", "City", "Éa", "名_", "GROSSE", "groẞe", "loadWorldMapLat",
	}, 1, 4)
}

// What the rules give, said outright: the reference above can't, where it
// uses the same code.
func TestScoreExamples(t *testing.T) {
	s := &scanner{}
	for _, c := range []struct {
		q, tok string
		want   int
	}{
		{"loadWorldMap", "loadWorldMap", 0},   // the name itself
		{"loadWorldMap", "LoadWorldMap", 1},   // other casing
		{"loadWorldMap", "load_world_map", 2}, // other separators
		{"load-world-map", "loadWorldMap", 2},
		{"loadWorldMap", "loadWorldMpa", 3}, // two letters swapped
		{"loadWorldMap", "loadWorldMa", 3},
		{"world", "wrold", 3},
		{"wold", "world", 0},                // a typo needs 5 letters
		{"loadWorldMap", "loadWorldCup", 3}, // two typos from 8 letters
		{"example", "exbmqle", 0},
		{"examples", "exbmqles", 3},
		{"loadMap", "loadCup", 8},      // two typos in 7 letters aren't alike, the verb is
		{"loadMap", "loadWorldMap", 4}, // its parts in order, with more
		{"load_map", "loadWorldMapData", 4},
		{"mapLoad", "loadWorldMap", 0},
		{"get_map", "getterWorldMap", 4}, // a part of three letters can be the start of one
		{"to_map", "tokenWorldMap", 0},
		{"City", "loadCityMap", 5},  // inside a longer name
		{"City", "loadCityMaps", 5}, // up to three times as long
		{"City", "loadCityMapsX", 0},
		{"Map", "mapper", 0},            // not for a name of three letters
		{"loadWorldMap", "WorldMap", 6}, // most of the name
		{"loadWorldMap", "World", 0},
		{"save_project", "load_project", 7}, // one part renamed, not the last
		{"rebuildFrontlineField", "invalidateFrontlineField", 7},
		{"save_project", "save_settings", 8}, // the same verb
		{"saveProject", "saveSettingsNow", 8},
		{"to_project", "to_settings", 0}, // a verb has three letters
		{"loadWorldMap", "unrelated", 0},
	} {
		if got := s.score(newQuery(c.q), []byte(c.tok)); got != c.want {
			t.Errorf("score(%q, %q) = %d, want %d", c.q, c.tok, got, c.want)
		}
	}
}

// Every rune that can be a letter or a number, every two bytes and a
// spread of longer sequences: after a name, before one and alone, the
// names are where the reference's regular expression has them.
func TestNamesAreTheReferencesRuneForRune(t *testing.T) {
	var data []byte
	check := func(b []byte) {
		t.Helper()
		data = append(append(append(append(append(data[:0], "ab"...), b...), "cd "...), b...), "ef"...)
		for _, d := range [][]byte{data, b} {
			want := refTokenRe.FindAllIndex(d, -1)
			k := 0
			for a, i := name(d, 0); a < len(d); a, i = name(d, i) {
				if k >= len(want) || want[k][0] != a || want[k][1] != i {
					t.Fatalf("%q: name %d is at %d-%d, in the reference %v", d, k, a, i, want)
				}
				k++
			}
			if k != len(want) {
				t.Fatalf("%q: %d names, in the reference %v", d, k, want)
			}
		}
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		check([]byte(string(r)))
		if r > 0x33FFF {
			r += 96 // no letters or numbers up there: a sample
		}
	}
	for a := range 256 {
		check([]byte{byte(a)})
		for b := range 256 {
			check([]byte{byte(a), byte(b)})
		}
	}
	for a := 0x80; a < 256; a++ {
		for b := 0x80; b < 0xC0; b += 3 {
			for c := 0; c < 256; c += 5 {
				check([]byte{byte(a), byte(b), byte(c)})
				check([]byte{byte(a), byte(b), byte(c), 0x80})
				check([]byte{byte(a), byte(b), byte(c), 0xbf, 'a'})
			}
		}
	}
}

// Every name of up to three symbols against every name of up to four, over
// small alphabets of what matters to the rules: separators, capitals, a
// digit, and letters whose lower case is shorter (İ, the Kelvin sign, ẞ)
// or depends on where they are (Σ).
func TestScoreAgreesWithTheReferenceOnSmallAlphabets(t *testing.T) {
	strs := func(alphabet []string, n int) []string {
		out, level := []string{""}, []string{""}
		for range n {
			var next []string
			for _, s := range level {
				for _, a := range alphabet {
					next = append(next, s+a)
				}
			}
			out, level = append(out, next...), next
		}
		return out
	}
	s := &scanner{}
	for _, alphabet := range [][]string{
		{"load", "Map", "MAP", "_", "s"},
		{"a", "B", "_", "İ"},
		{"a", "B", "1", "$"},
		{"ab", "ǅ", "Σ", "ς"},
		{"ab", "\u212a", "ẞ", "ß"},
		{"save", "World", "-", "é"},
	} {
		var toks []string
		for _, x := range strs(alphabet, 4) {
			if len(x) >= 3 && refTokenRe.FindString(x) == x {
				toks = append(toks, x)
			}
		}
		var alike [9]int
		for _, q := range strs(alphabet, 3) {
			nq := newQuery(q)
			for _, tok := range toks {
				want := refScore(q, tok)
				alike[want]++
				if got := s.score(nq, []byte(tok)); got != want {
					t.Fatalf("score(%q, %q) = %d, in the reference %d", q, tok, got, want)
				}
			}
		}
		for sc, n := range alike {
			if n == 0 && alphabet[0] == "load" && sc != 7 {
				t.Errorf("%q: no pair of names scores %d", alphabet, sc)
			}
		}
	}
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
