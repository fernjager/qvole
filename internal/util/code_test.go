package util

import (
	"strings"
	"testing"

	"github.com/fernjager/qvole/spake2"
)

func TestCodeWords_Count(t *testing.T) {
	if len(spake2.CodeWords) != 7766 {
		t.Fatalf("expected 7766 words, got %d", len(spake2.CodeWords))
	}
}

func TestGenerateCode_Format(t *testing.T) {
	for i := 0; i < 100; i++ {
		code, err := GenerateCode()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		first := strings.SplitN(code, "-", 2)[0]
		if len(first) != 4 {
			t.Fatalf("expected 4-digit number, got %q in %s", first, code)
		}
	}
}

func TestGenerateCode_WordsInList(t *testing.T) {
	wordSet := make(map[string]bool, len(spake2.CodeWords))
	for _, w := range spake2.CodeWords {
		wordSet[w] = true
	}
	for i := 0; i < 100; i++ {
		code, err := GenerateCode()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		np := Nameplate(code)
		rest := code[len(np)+1:]
		// Some words contain hyphens (e.g. "t-shirt"). Parse greedily: for
		// each position in rest, try matching the longest word from the list.
		for len(rest) > 0 {
			var found bool
			for _, w := range spake2.CodeWords {
				if strings.HasPrefix(rest, w) && (len(rest) == len(w) || rest[len(w)] == '-') {
					rest = rest[len(w):]
					if len(rest) > 0 {
						rest = rest[1:] // skip hyphen
					}
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("could not parse words in %q (remaining: %q)", code, rest)
			}
		}
	}
}

func TestGenerateCode_DeterministicNot(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		code, err := GenerateCode()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if seen[code] {
			t.Fatalf("duplicate code generated: %s", code)
		}
		seen[code] = true
	}
}

func TestNameplate_ExtractsPrefix(t *testing.T) {
	np := Nameplate("9908-ability-subway-unicorn")
	if np != "9908" {
		t.Fatalf("expected 9908, got %q", np)
	}
}

func TestNameplate_LeadingZeros(t *testing.T) {
	np := Nameplate("0000-ability-subway-unicorn")
	if np != "0000" {
		t.Fatalf("expected 0000, got %q", np)
	}
}

func TestNameplate_FiveDigitPrefix(t *testing.T) {
	np := Nameplate("12345-test")
	if np == "12345" || len(np) != 8 {
		t.Fatalf("expected 8-char hash for 5-digit prefix, got %q (len=%d)", np, len(np))
	}
}

func TestNameplate_NoDash(t *testing.T) {
	np := Nameplate("hello")
	if np != "870dcb1f" {
		t.Fatalf("expected 870dcb1f, got %q", np)
	}
}

func TestNameplate_Empty(t *testing.T) {
	np := Nameplate("")
	if np != "72786c0e" {
		t.Fatalf("expected 72786c0e, got %q", np)
	}
}

func TestRandInt_ValidRange(t *testing.T) {
	for max := 1; max <= 256; max++ {
		v, err := randInt(max)
		if err != nil {
			t.Fatalf("randInt(%d): unexpected error: %v", max, err)
		}
		if v < 0 || v >= max {
			t.Fatalf("randInt(%d) = %d, want [0, %d)", max, v, max)
		}
	}
}

func TestRandInt_MaxBoundary(t *testing.T) {
	v, err := randInt(65536)
	if err != nil {
		t.Fatalf("randInt(65536): %v", err)
	}
	if v < 0 || v >= 65536 {
		t.Fatalf("randInt(65536) = %d, want [0, 65536)", v)
	}
}

func TestRandInt_AlwaysZero(t *testing.T) {
	v, err := randInt(1)
	if err != nil {
		t.Fatalf("randInt(1): %v", err)
	}
	if v != 0 {
		t.Fatalf("randInt(1) = %d, want 0", v)
	}
}

func TestRandInt_Uniformity(t *testing.T) {
	const N = 10000
	const buckets = 10
	counts := make([]int, buckets)
	for i := 0; i < N; i++ {
		v, err := randInt(buckets)
		if err != nil {
			t.Fatalf("randInt: %v", err)
		}
		counts[v]++
	}
	expected := N / buckets
	for i, c := range counts {
		if c < expected/2 || c > expected*2 {
			t.Errorf("bucket %d got %d samples (expected ~%d)", i, c, expected)
		}
	}
}

func TestRandInt_OutOfRange(t *testing.T) {
	cases := []int{0, -1, -100, 65537, 100000}
	for _, max := range cases {
		if _, err := randInt(max); err == nil {
			t.Errorf("randInt(%d): expected error, got nil", max)
		}
	}
}

func TestNameplate_GeneratedCodeRe(t *testing.T) {
	matchCases := []string{"0000-", "9999-", "1234-abc"}
	for _, c := range matchCases {
		if !GeneratedCodeRe.MatchString(c) {
			t.Errorf("expected regex to match %q", c)
		}
	}
	noMatchCases := []string{"123-", "12345-", "abcd-", "1234", "", "0000"}
	for _, c := range noMatchCases {
		if GeneratedCodeRe.MatchString(c) {
			t.Errorf("expected regex to not match %q", c)
		}
	}
}
