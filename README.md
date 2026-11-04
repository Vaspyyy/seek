<div align="center">

# seek

A code search that knows which function each hit is in.

[![Go](https://img.shields.io/github/go-mod/go-version/Vaspyyy/seek?style=flat-square&labelColor=1e1e2e&color=89b4fa&logo=go&logoColor=89b4fa)](go.mod)
[![Release](https://img.shields.io/github/v/tag/Vaspyyy/seek?style=flat-square&labelColor=1e1e2e&color=cba6f7&label=release)](https://github.com/Vaspyyy/seek/tags)
[![License](https://img.shields.io/github/license/Vaspyyy/seek?style=flat-square&labelColor=1e1e2e&color=a6e3a1)](LICENSE)

<img src="assets/hero.gif" alt="rg printing a flat list of lines, then seek grouping the same hits by function, then seek -s printing the functions" width="900">

</div>

seek searches a project like `rg` or `grep -rn`, but puts every hit under the function, method, class or section that contains it, with that definition's line range. It labels hits in comments and strings, finds definitions (`-d`), prints the containing definitions (`-s`), keeps its output within a line budget, and says what it left out: hits inside longer words, files it didn't search, code the budget didn't fit. When nothing matches, it suggests existing names that look like the one you typed, with where they're defined.

It was written for AI coding agents. In an agent's work a search is usually followed by a read of the code around a hit, because a line number alone doesn't say which function it is in; seek's output answers that directly. For people it adds colors and works as a quicker overview than a flat list of lines.

## Install

```sh
go install github.com/Vaspyyy/seek@latest
```

You'll need Go 1.26 or newer (older versions fetch the right toolchain on their own) and git. seek is a single binary. It's developed on Linux and should work on macOS and the BSDs; Windows isn't supported.

## Usage

```sh
seek PATTERN [PATH...]
```

A pattern is a regular expression in Go's syntax. One that is an identifier, or several joined by `|` (`setSpeed`, `Repo.Open`, `load|save`), matches whole words only and is case-sensitive, so `seek speed` doesn't find `speedMult` or `Speed`. When other casings or longer words match too, the line right under the header says how many and which, with the command that includes them (`-i --partial`). A dot stays a regex dot, so `lim.t` finds `limit`. A pattern that matches the empty string, such as `a|`, matches every line, and the header says so. A pattern that isn't a valid regex, such as `foo(`, is searched as plain text, and the header says so.

| Command | Shows |
| --- | --- |
| `seek X` | every hit, grouped by the definition that contains it |
| `seek -d X` | only where X is defined, one line each (also `Type.method`, fields, module-level constants and, for a trait, its `impl` blocks) |
| `seek -s X` | the containing definitions in full, or the lines around the hits in long ones |
| `seek -c X` | one line per definition with its hit count and line numbers |
| `seek -l X` | only the paths of files with hits (`-0` separates them with NUL bytes; with `-d`, the files that define X) |
| `seek --writes X` | only the hits that set X: definitions, declarations and assignments (not getters or destructuring keys) |
| `seek -C 3 X` | three lines of context around each hit |

`-i` ignores case, `-F` takes the pattern as plain text, `-w` makes a regex match whole words, `-g GLOB` limits the search to matching files (`-g '*.ts' -g '!*.test.ts'`), and `-b N` sets roughly how many lines the output may use (100 by default, 300 with `-s`, 0 for no limit). The files searched are what git sees in a repository, tracked and untracked but not ignored; outside a repository, caches, build output and dot-folders are skipped, and `-u` includes all of them. A path you name is searched even when git ignores it, and when nothing matches, seek says which folders and binary files it skipped. A qualified name that never appears as text, such as `Repo.Changes` for `func (r *Repo) Changes`, is pointed to its definition. `-A` gives plain output without colors, which is the default when stdout isn't a terminal. The exit status is 0 if something matched, 1 if not and 2 on errors, as with grep.

## What it prints

```
seek: "retreat" (whole word): 7 hits in 4 files
+11 more hits in another case or inside longer words (RETREAT_MORALE 3, RETREAT_SPEED 3, shouldRetreat 3, retreatVector 2, +1 more): seek -i --partial retreat
src/ui.js  10 lines
  function bindUi.sim.worker.onmessage  L2-6
     4 const fleeing = units.filter((u) => u.state === "retreat").length;  (string)
     5 document.title = `${fleeing} units in retreat`;  (string)
  function bindUi  L1-10
     8 sim.setSpeed(Number(e.target.value)); // the retreat speed is fixed  (comment)
src/units.js  33 lines
  function createUnitUpdater.shouldRetreat  L4-12
     6 * The retreat vector points away from the nearest enemies.  (comment)
  function createUnitUpdater.step  L19-30
    21 unit.state = "retreat";  (string)
tests/units.test.js  8 lines
  (top level)
    7 expect(u.state).toBe("retreat");  (string)
README.md  4 lines
  h1 skirmish  L1-4
    4 they are outnumbered, then retreat away from the nearest enemies.
```

Each file gets a line with its path and size. Below it come the definitions with hits, named the way they nest (`Type.method`, `outer.inner`, `worker.onmessage` for a callback assigned to a property), with their line range, and then the hit lines with the nesting they have in the file. A doc comment belongs to the definition below it, and the range always starts at the comment, so a definition has the same range in every view. Hits outside any definition are listed as `(top level)`. In a definition longer than 150 lines, each hit comes after the line that opens its block, such as the `if` or loop around it, so a 1500-line factory function still says where in it a hit is.

When everything doesn't fit the budget, files with a definition of the pattern come first, then source, tests, documentation and data, and last lock files, generated, minified and vendored code. Each file gets a line first; then, in that order, a line per definition with its hit count and line numbers; then the hit lines. With `-s`, files get their definitions whole in order, or the lines around their hits; with `-C`, files get as much context as fits, down to none, but always their hit lines. A closing line says how many files got less than asked for.

The labels are heuristics on the text of a line, not a parser. `(comment)` and `(string)` mark hits in comments and string literals, also in block comments and docstrings that span lines; code inside `${…}` in a template string or `{…}` in a Python f-string counts as code. Where hit lines aren't shown, the counts say what the hits were, such as `4 uses, 2 definitions, 3 assignments, 1 in a comment`, and add up to the total.

Definitions come from the outline rules of [glint](https://github.com/Vaspyyy/glint), which cover Go, Rust, Python, JavaScript and TypeScript, C and C++, Java, Kotlin, C#, GDScript, shell, Lua, Ruby and Markdown headings. In other files the hits are listed without grouping.

## For coding agents

When its output isn't a terminal, seek prints plain text, so an agent can use it directly. To make it the default, add a line like this to the agent's instructions (`AGENTS.md`, `CLAUDE.md` or similar):

```markdown
- To search code, prefer `seek PATTERN [path]` over grep and rg: it groups hits by the function or
  class that contains them, with line ranges, so a follow-up read is rarely needed. `seek -d Name`
  finds definitions, `seek -s Name` prints the containing functions, and when nothing matches it
  suggests similar existing names.
```

To see whether it helps, I replayed the searches agents had made in one JavaScript project over several weeks (1,484 distinct grep calls) against the project's current code. seek's output alone was not smaller than grep's: grouping adds a line per definition, so small results grow and large ones shrink.

| Hits per search | Searches | grep output | seek output |
| --- | ---: | ---: | ---: |
| 1 to 5 | 411 | 21,970 tokens | 41,152 tokens |
| 6 to 20 | 236 | 56,481 tokens | 69,850 tokens |
| 21 to 99 | 166 | 157,373 tokens | 148,933 tokens |
| 100 (grep stops there) | 69 | 136,576 tokens | 114,696 tokens |

The saving is in what comes after. In the same sessions, a search was followed by a read of the code around a hit 1,417 times, and those reads cost two and a half times as much as the searches. For the 403 searches that were followed by such reads and still match today's code, grep plus the reads came to 982,920 tokens; seek's overview alone, which names the enclosing function and its range, came to 153,313 (84% less), and `seek -s`, which prints the code itself, to 564,722 (43% less). Of the 602 old searches that find nothing in today's code, seek offers a lead for 520: a name in another case or spelling, a longer word, or a definition whose name contains the word.

Before release, seek went through review rounds in which several models used it on real projects to find definitions, callers and assignments, fix a planted bug and do a rename, compared it with rg, and tried to break it. What they found is covered by tests that build real files and git repositories.

## Limits

seek matches text, line by line; it doesn't know types or scopes. `seek -d X` lists definitions named X that the outline rules or a defining keyword show, which misses definitions made by macros or by unusual syntax, and local variables inside functions don't count. Look-around and backreferences aren't supported by Go's regular expressions; `rg -P` has them. Binary files and files over 32 MB aren't searched.

## License

GPL-3.0. See [LICENSE](LICENSE).
