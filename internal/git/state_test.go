package git

import "testing"

func TestParsePorcelainV2Z(t *testing.T) {
	t.Parallel()

	input := "1 M. N... 100644 100644 100644 a a staged.txt\x00" +
		"1 .M S.M. 100644 100644 100644 b b unstaged.txt\x00" +
		"2 R. N... 100644 100644 100644 c c R100 renamed.txt\x00old.txt\x00" +
		"u UU N... 100644 100644 100644 100644 d d d conflict.txt\x00" +
		"? untracked.txt\x00! ignored.txt\x00"
	got, err := parsePorcelainV2Z(input)
	if err != nil {
		t.Fatal(err)
	}
	want := WorkingTreeStatus{
		Staged:          2,
		Unstaged:        1,
		Untracked:       1,
		Ignored:         1,
		Conflicted:      1,
		DirtySubmodules: 1,
	}
	if got != want {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
}

func TestParsePorcelainV2ZRejectsIncompleteRename(t *testing.T) {
	t.Parallel()

	_, err := parsePorcelainV2Z("2 R. N... fields\x00")
	if err == nil {
		t.Fatal("incomplete rename record was accepted")
	}
}
