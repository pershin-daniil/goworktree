package work

import (
	"strings"
	"testing"
)

func TestParseName(t *testing.T) {
	t.Parallel()

	valid := []string{"EVOVPC-3048", "work_1", "a.b", "0", "A"}
	for _, input := range valid {
		input := input
		t.Run("valid_"+input, func(t *testing.T) {
			t.Parallel()
			got, err := ParseName(input)
			if err != nil {
				t.Fatalf("ParseName(%q): %v", input, err)
			}
			if got.String() != input {
				t.Fatalf("name changed: got %q, want %q", got, input)
			}
		})
	}

	invalid := []string{
		"", ".", "..", " leading", "trailing ", "a/b", `a\b`,
		"-leading", "_leading", ".leading", "a:b", "ветка",
		strings.Repeat("a", MaxNameBytes+1),
	}
	for _, input := range invalid {
		input := input
		t.Run("invalid", func(t *testing.T) {
			t.Parallel()
			if _, err := ParseName(input); err == nil {
				t.Fatalf("ParseName(%q) succeeded", input)
			}
		})
	}
}
