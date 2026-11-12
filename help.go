package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// help colors, the same 256-color palette as glint's, crux's and recap's help
const (
	hTitle   = "\x1b[38;5;183;1m"
	hDim     = "\x1b[38;5;59m"
	hTagline = "\x1b[38;5;146;3m"
	hSection = "\x1b[38;5;111;1m"
	hShort   = "\x1b[38;5;151m"
	hLong    = "\x1b[38;5;117m"
	hText    = "\x1b[38;5;189m"
	hReset   = "\x1b[0m"
)

type helpFlag struct{ short, long, arg, text string }

type helpPair struct{ name, text string }

// helpPattern says what a pattern is, and helpAfter what else to know;
// paragraphs are kept as written.
var helpPattern = []string{
	`A regular expression (Go syntax). One that is an identifier, or several
joined by | ("setSpeed", "Repo.Open", "a|b|c"), matches whole words only.
An invalid regex is searched as plain text.`,
	`A ref to a file seek can find ("internal/files/files.go:List",
"files.go:globSet.match", "main.go:104") shows that definition (for a
line: the innermost one containing it), then the hits of its name
elsewhere, grouped as usual. With -F or -e the pattern is always a pattern.`,
}

var helpAfter = []struct {
	title string
	paras []string
}{
	{"CONTEXT", []string{
		`On by default: definitions changed in the working tree [changed] or on
this branch [branch], in the commit recap last showed [focus] or failing
in crux's last run [failing] get that mark after their name and come
first. Nothing is left out for it. A "context:" line on top says what
was used.`,
		`In ref mode, hits in tests are labelled "(test, mentions NAME)", and a
"next:" line at the bottom gives the crux command that runs those tests.`,
	}},
	{"FILES", []string{
		`What git sees in a repository (tracked and untracked, not ignored);
elsewhere caches, build output and dot-folders are skipped. Paths are
printed relative to the repository root.`,
	}},
}

var helpSections = []struct {
	title string
	flags []helpFlag
}{
	{"WHAT TO SHOW", []helpFlag{
		{"-d", "", "", "definitions only: where PATTERN is defined, one line each"},
		{"-s", "", "", "show the whole definition around each hit (windows in long ones)"},
		{"-c", "", "", "counts: at most one line per definition"},
		{"-l", "", "", "only the paths of files with hits (with -d: the files that define it)"},
		{"-0", "", "", "with -l: paths separated by NUL bytes"},
		{"-C", "", "N", "N lines of context around each hit (with -s: the window size)"},
	}},
	{"MATCHING", []helpFlag{
		{"-i", "", "", "ignore case"},
		{"-F", "", "", "plain text, not a regex"},
		{"-w", "", "", "whole words, also for a regex"},
		{"", "--partial", "", "identifiers also inside longer words (speed finds speedMult)"},
		{"", "--writes", "", "only hits that set the name: definitions, declarations, assignments"},
		{"-e", "", "PATTERN", `a pattern that starts with "-" (or that isn't a ref)`},
	}},
	{"WHERE", []helpFlag{
		{"-g", "", "GLOB", `only files matching GLOB ("*.js", "src/**", "!*_test.go"); repeatable`},
		{"-u", "", "", "also ignored files, caches and dot-folders"},
	}},
	{"OUTPUT", []helpFlag{
		{"-b", "", "N", "about how many lines to print (default 100, with -s 300; 0: no limit)"},
		{"-A", "", "", "plain output without colors (the default when not on a terminal)"},
		{"", "--json", "", "one JSON document instead (Prism's report.Seek), every hit unless -b N"},
	}},
	{"OTHER", []helpFlag{
		{"", "--no-context", "", "ignore and don't record context; also PRISM_NO_CONTEXT=1"},
		{"-h", "--help", "", "show this help"},
		{"-V", "--version", "", "show the version"},
	}},
}

var helpExamples = []helpPair{
	{"seek setSpeed", "every hit of the word, grouped by the definition around it"},
	{"seek -d Repo", "where Repo is defined"},
	{"seek -s parse internal/", "the whole definitions that mention parse"},
	{"seek files.go:List", "that definition, then the hits of its name elsewhere"},
	{"seek -e -verbose", "a pattern that starts with -"},
}

var helpNotes = []helpPair{
	{"exit code", "0 if something matched, 1 if not, 2 on errors"},
	{"NO_COLOR", "set it to turn colors off"},
}

// helpText renders the help, colored like recap's when color is true.
func helpText(color bool) string {
	paint := func(code, s string) string {
		if !color || s == "" {
			return s
		}
		return code + s + hReset
	}
	const flagWidth = 24
	var b strings.Builder
	section := func(title string) {
		fmt.Fprintf(&b, "\n  %s\n", paint(hSection, title))
	}
	pairs := func(ps []helpPair, w int, nameColor, textColor string) {
		for _, p := range ps {
			w = max(w, len(p.name))
		}
		for _, p := range ps {
			pad := strings.Repeat(" ", w-len(p.name)+2)
			fmt.Fprintf(&b, "    %s%s%s\n", paint(nameColor, p.name), pad, paint(textColor, p.text))
		}
	}

	fmt.Fprintf(&b, "\n  %s %s  %s\n", paint(hTitle, "✦ seek"), paint(hDim, "v"+version),
		paint(hTagline, "search code, with hits grouped by the definition that contains them"))

	section("USAGE")
	fmt.Fprintf(&b, "    seek %s %s %s\n", paint(hDim, "[flags]"), paint(hLong, "PATTERN"), paint(hDim, "[PATH...]"))

	prose := func(title string, paras []string) {
		section(title)
		for i, p := range paras {
			if i > 0 {
				b.WriteString("\n")
			}
			for _, line := range strings.Split(p, "\n") {
				fmt.Fprintf(&b, "    %s\n", paint(hText, line))
			}
		}
	}
	prose("PATTERN", helpPattern)

	for _, s := range helpSections {
		section(s.title)
		for _, f := range s.flags {
			left, plain := "    ", "    "
			if f.short != "" {
				left = paint(hShort, f.short)
				plain = f.short
				if f.long != "" {
					left += paint(hDim, ", ")
					plain += ", "
				}
			}
			if f.long != "" {
				left += paint(hLong, f.long)
				plain += f.long
			}
			if f.arg != "" {
				left += " " + paint(hDim, f.arg)
				plain += " " + f.arg
			}
			fmt.Fprintf(&b, "    %s%s%s\n", left, strings.Repeat(" ", max(flagWidth-len(plain), 2)), paint(hText, f.text))
		}
	}

	for _, a := range helpAfter {
		prose(a.title, a.paras)
	}
	section("EXAMPLES")
	pairs(helpExamples, flagWidth-2, hLong, hDim)
	section("GOOD TO KNOW")
	pairs(helpNotes, 0, hLong, hDim)
	b.WriteString("\n")
	return b.String()
}

// wantsColor reports whether text written to f may be colored.
func wantsColor(f *os.File) bool {
	return term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == ""
}
