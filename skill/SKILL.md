---
name: seek
description: Search code with `seek` instead of the grep tool, `grep -rn` or `rg` when you look for where something is defined, used, set or mentioned in a project. It groups hits by the function, class or section that contains them (with line ranges), so you rarely need a follow-up read to see the context; `seek FILE:NAME` prints one definition and then its callers, `seek -s` prints the containing definitions whole, `seek -d` finds only definitions, and when nothing matches it suggests similar names that do exist. Use it whenever you would grep source code.
---

# seek for agents

`seek PATTERN [PATH...]` searches the files git sees (tracked and untracked, not ignored) and prints each hit under the definition that contains it:

```
seek: "lastPart" (whole word): 19 hits in 5 files
internal/group/group.go  691 lines
  func lastPart  L448-453
    448 func lastPart(name string) string {
  func isDef  L645-672
    652 names := []string{lastPart(s.Name)}
    666     if name != "" && strings.EqualFold(lastPart(h.Text[a:b]), name) {
  func bare  L675-680
    679 return lastPart(strings.TrimSpace(t))
internal/suggest/suggest.go  433 lines
  func Similar  L39-62
     45 qs[i] = newQuery(lastPart(w))
```

Often the overview already answers the question, because it says which function each use is in. The line ranges are ready to use with the read tool (`offset` = first line), and a file's path, a colon and a definition's name is a ref you can hand back to seek (`internal/group/group.go:isDef`).

## What to run

| Question | Command |
| --- | --- |
| Where is X used, and in which functions? | `seek X` |
| Several names at once | `seek 'a|b|c'` |
| Show me this definition, and who calls it | `seek path/file.go:Name` (see "Refs" below) |
| Which function is this line in (a compiler's or a test's `file:line`)? | `seek path/file.go:104` |
| Where is X defined? | `seek -d X` (one line each with its ref, lines and signature; also `Type.method`, fields, module-level constants and, for a trait, its `impl` blocks) |
| Show me the code around each hit | `seek -s X` (whole definitions, or the lines around the hits in long ones) |
| How much is X used, per function? | `seek -c X` |
| Which files mention X? | `seek -l X` (`-0` separates them with NUL bytes; `-l -d X` for the files that define it) |
| Where is X set? | `seek --writes X` (only definitions, declarations and assignments, not getters or destructuring keys) |
| Only some files | `seek X src/` or `seek -g '*.ts' -g '!*.test.ts' X` |
| Lines around each hit | `seek -C 3 X` (with `-s`, the window size in long definitions) |

A pattern is a Go regular expression. One that is an identifier, or several joined by `|`, matches whole words only and is case-sensitive: `seek speed` doesn't find `speedMult` or `Speed`. A dot stays a regex dot (`lim.t` finds `limit`). When other casings or longer words match too, the line right under the header says how many and which, with the command that includes them (`+25 more hits in another case (options 27): seek -i Options`); run that when you're exploring a concept and not one name. A pattern that matches the empty string (`a|`) matches every line, and one that isn't a valid regex (`foo(`) is searched as plain text; the header says so in both cases.

`-i` ignores case, `--partial` finds identifiers inside longer words too, `-F` takes the pattern as plain text, `-w` makes a regex match whole words, `-u` also searches ignored files, caches and dot-folders, and `-e PATTERN` is for a pattern that starts with `-` or looks like a ref and isn't one. With `-d`, an identifier stays a whole word: `seek -d last` doesn't list `lastPart`, but names it under "definitions whose names contain it", and `seek -d --partial last` lists them all.

## Refs

A pattern that is a ref to a file shows what the ref names. `seek internal/suggest/suggest.go:lastPart` prints that definition as `-s` would, doc comment included, then the hits of `lastPart` elsewhere in the repository, grouped as usual (its callers and mentions, and here other functions with its name):

```
seek: internal/suggest/suggest.go:lastPart (func, L250-255), then 18 hits of "lastPart" in 5 files
internal/suggest/suggest.go  433 lines
  func lastPart  L250-255
    250: func lastPart(name string) string {
    251- 	if i := strings.LastIndexAny(name, ".:"); i >= 0 {
    252- 		return name[i+1:]
    253- 	}
    254- 	return name
    255- }
internal/group/group.go  691 lines
  func lastPart  L448-453
    448 func lastPart(name string) string {
  func isDef  L645-672
    652 names := []string{lastPart(s.Name)}
```

- The path can be relative to the repository root or to the working directory, or just its end (`suggest.go:lastPart`). The name can be the end of a qualified one (`files.go:match` for `globSet.match`).
- A line (`main.go:104`, `main.go:104:7`, as compilers and crux print it) stands for the innermost definition containing it.
- A name that fits several definitions shows all of them; one that fits none lists the definitions of the file it might mean.
- A name defined more than once in a file is `parse#1`, `parse#2`.
- A pattern whose path isn't a file is a regex as usual (`seek 'todo:fix'` searches text). A ref to a commit (`main.go:run@b681ef1`) is for recap: seek searches the working tree.

glint, seek, recap and crux print and accept the same refs, so what one of them prints can be handed to another as is: a line from `crux go test` to `seek`, a ref from `seek -d` to `recap --show`.

## Context and next: lines

seek knows what you're working on without being told. After a definition's name, `[changed]` means it changed in the working tree, `[branch]` that it was committed on this branch, `[focus]` that the commit recap last showed changed it, and `[failing]` that it failed in crux's last run. Marked definitions and their files come first. Nothing is left out for it. When anything was marked or reordered, the first line says what was used:

```
context: 1 changed symbol vs HEAD; focus: commit b681ef1 (recap, 4 min ago)
```

In ref mode, hits in Go, pytest and Rust tests are labelled `(test, mentions NAME)`, and a `next:` line at the bottom is the command that runs those tests, to run as printed:

```
next: crux go test -run '^TestList$' ./internal/files/   runs the 1 test that mentions List
```

"mentions" is all seek knows: the test names the definition, which doesn't say it tests it.

`--no-context` (or `PRISM_NO_CONTEXT=1`) ignores context and records nothing; you shouldn't need it.

## Reading the output

- The header gives the pattern as seek read it, the number of hits and files, and, when the output doesn't show them, where the pattern is defined (`; defined at internal/search/search.go:42`).
- Each file gets a line with its path and size. Below it come the definitions with hits (`func name  Lfirst-last`, qualified as `Type.method` or `outer.inner`, a callback assigned to a property as `worker.onmessage`), then the hit lines with their line numbers and the nesting they have in the file. `(top level)` holds hits outside any definition.
- A range starts at the definition's own line, as compilers and editors report it, the same in every view and in glint, recap and crux. A doc comment belongs to the definition below it: a hit in it is listed there, and `-s` prints it, but it isn't part of the range.
- In a definition longer than 150 lines, each hit comes after the line that opens its block (the `if`, loop or callback around it), so you can tell where in it the hit is.
- `(comment)` and `(string)` mark hits in a comment or a string literal, also in ones that span lines; code in `${…}`, `$(…)` and f-string `{…}` counts as code. Where hit lines aren't shown, counts say what the hits were (`4 uses, 2 definitions, 3 assignments, 1 in a comment`; they add up to the total). Labels are heuristics on the text, not a parser.
- Under the budget (`-b N`, default 100 lines, 300 with `-s`, 0 for no limit), files with a definition of the pattern come first, then source, tests, documentation and data, and last lock files, generated, minified and vendored code. When everything doesn't fit, each file gets a line first, then a line per definition with its hit count and line numbers (`func isDef  L645-672  2 hits: L652, 666`), then the hit lines. Raise `-b`, add a path or `-g`, or use `-c` deliberately.
- With `-s`, `-C` and in ref mode, lines use grep's notation: `104:` is a hit, `105-` context, `…` a gap. The text is the file's exact text (tabs included), so it can be used for edits. When the budget keeps `-s` or `-C` from showing everything, a closing line says how many files got less.
- When nothing matches, seek says what would have, and what it didn't search (binary files, ignored folders, caches and dot-folders). A path you name is searched even when git ignores it. A qualified name whose text never appears (`Repo.Changes` for `func (r *Repo) Changes`) is pointed to its definition. Otherwise it offers the same pattern ignoring case, the name inside longer words, and similar names with where they're defined (other casing, snake_case for camelCase, typos, `loadMap` for `loadWorldMap`):

  ```
  seek: "keep_name" (whole word) in 26 files: no hits
    not searched: 1 binary file (assets/hero.gif)
    similar names:
      keepName  5 hits, defined at main.go:874 (func)
  ```

- The exit status is 0 if something matched, 1 if not and 2 on errors, as with grep.

`--json` prints one JSON document instead of the text, in every mode, for programs (Prism's `report.Seek`, https://github.com/Vaspyyy/prism/blob/main/docs/output.md). Read the text yourself: it's shorter, and it says what it left out.

## Limits

seek matches text, line by line; it doesn't know types or scopes. Definitions come from Prism's outline rules, shared with glint, recap and crux (Go, Rust, Python, JS/TS, C/C++, Java, Kotlin, C#, GDScript, shell, Lua, Ruby, Markdown headings); other files list their hits without grouping. `seek -d X` misses definitions made by macros or unusual syntax, and local variables inside functions don't count. Binary files and files over 32 MB aren't searched. For logs and huge data files, or for look-around and backreferences, use `rg` (`rg -P`).
