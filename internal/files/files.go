// Package files lists the files seek searches: what git sees in a
// repository (tracked and untracked, not ignored), and a walk that skips
// caches and dot-folders elsewhere.
package files

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Options select the files.
type Options struct {
	Globs      []string // "*.js", "src/**/*.go", "!*_test.go"
	Unrestrict bool     // also ignored files, caches and dot-folders
}

// skipDirs are never descended into outside a git repository (and with
// -u only .git is).
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "target": true, "__pycache__": true, ".venv": true, "venv": true,
	".mypy_cache": true, ".pytest_cache": true, ".ruff_cache": true, ".tox": true, ".gradle": true,
	".idea": true, ".cache": true, "dist": true, "build": true, ".next": true, ".godot": true,
}

// List returns the files below roots (files or directories), as paths
// joined to the root they were found under, sorted. Roots that are files
// are always included.
func List(roots []string, o Options) ([]string, error) {
	if len(roots) == 0 {
		roots = []string{"."}
	}
	globs, err := compile(o.Globs)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string, explicit bool) {
		p = filepath.Clean(p)
		if seen[p] || !explicit && !globs.match(filepath.ToSlash(p)) {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, root := range roots {
		st, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			add(root, true)
			continue
		}
		if err := walk(root, o, add); err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func walk(root string, o Options, add func(string, bool)) error {
	if !o.Unrestrict {
		if ps, ok := gitFiles(root); ok {
			for _, p := range ps {
				if st, err := os.Lstat(filepath.Join(root, p)); err == nil && st.Mode().IsRegular() {
					add(filepath.Join(root, p), false)
				}
			}
			return nil
		}
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // unreadable: skip
		}
		name := d.Name()
		if d.IsDir() {
			if p == root {
				return nil
			}
			if name == ".git" || !o.Unrestrict && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			if !o.Unrestrict {
				if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
					// a repository inside a plain folder: use git's view of it
					if ps, ok := gitFiles(p); ok {
						for _, f := range ps {
							if st, err := os.Lstat(filepath.Join(p, f)); err == nil && st.Mode().IsRegular() {
								add(filepath.Join(p, f), false)
							}
						}
						return filepath.SkipDir
					}
				}
			}
			return nil
		}
		if d.Type().IsRegular() && (o.Unrestrict || !strings.HasPrefix(name, ".") || name == ".env.example") {
			add(p, false)
		}
		return nil
	})
}

// gitFiles lists what git sees below dir (relative to dir), or false if
// dir isn't in a git work tree.
func gitFiles(dir string) ([]string, bool) {
	cmd := exec.Command("git", "-C", dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--deduplicate")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	b, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var out []string
	for _, f := range bytes.Split(b, []byte{0}) {
		if len(f) > 0 {
			out = append(out, string(f))
		}
	}
	return out, true
}

type globSet struct{ include, exclude []*regexp.Regexp }

func (g globSet) match(p string) bool {
	p = strings.TrimPrefix(p, "./")
	hit := func(res []*regexp.Regexp) bool {
		for _, re := range res {
			if re.MatchString(p) {
				return true
			}
		}
		return false
	}
	if hit(g.exclude) {
		return false
	}
	return len(g.include) == 0 || hit(g.include)
}

func compile(globs []string) (globSet, error) {
	var g globSet
	for _, s := range globs {
		neg := strings.HasPrefix(s, "!")
		re, err := globRe(strings.TrimPrefix(s, "!"))
		if err != nil {
			return g, err
		}
		if neg {
			g.exclude = append(g.exclude, re)
		} else {
			g.include = append(g.include, re)
		}
	}
	return g, nil
}

// globRe turns a glob into a regexp: * and ? stay within a path element,
// ** crosses them, {a,b} picks one. It matches a path from any folder on
// ("*.js", "testdata/**"), or from the root if it starts with a slash; a
// folder matches everything in it.
func globRe(g string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	if !strings.HasPrefix(g, "/") && !strings.HasPrefix(g, "**/") {
		b.WriteString("(?:.*/)?")
	}
	g = strings.TrimPrefix(g, "/")
	depth := 0
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case c == '*' && i+1 < len(g) && g[i+1] == '*':
			i++
			if i+1 < len(g) && g[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '{':
			depth++
			b.WriteString("(?:")
		case c == '}' && depth > 0:
			depth--
			b.WriteString(")")
		case c == ',' && depth > 0:
			b.WriteString("|")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("(?:/.*)?$") // a directory matches what's in it
	return regexp.Compile(b.String())
}
