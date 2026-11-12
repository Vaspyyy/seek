package main

import (
	"strings"
	"testing"
)

// The tools agree on -V for the version, on how an unknown flag is told and
// on the help's banner; -v is not seek's, so it is unknown rather than a guess.
func TestVersionAndUnknownFlag(t *testing.T) {
	for _, flag := range []string{"-V", "--version"} {
		if _, code, msg := parse([]string{flag}); code != 0 || msg != "seek "+version+"\n" {
			t.Errorf("seek %s: code %d, msg %q", flag, code, msg)
		}
	}
	for _, flag := range []string{"-v", "--bogus"} {
		if _, code, msg := parse([]string{flag}); code != 2 || msg != "unknown flag "+flag+" (see seek --help)" {
			t.Errorf("seek %s: code %d, msg %q", flag, code, msg)
		}
	}
	if _, code, msg := parse([]string{"-h"}); code != 0 || !strings.HasPrefix(msg, "\n  ✦ seek v"+version+"  ") || !strings.Contains(msg, "-V, --version") {
		t.Errorf("seek -h: code %d, help starts %q", code, msg[:min(len(msg), 60)])
	}
}
