package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestPickerKeyBindingsIncludeVim(t *testing.T) {
	for _, want := range []string{"j", "k", "g", "G", "/", "enter", "esc"} {
		found := false
		for _, k := range PickerKeyBindings(false) {
			if k == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing key %q", want)
		}
	}
	found := false
	for _, k := range PickerKeyBindings(true) {
		if k == "space" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("multi picker missing space toggle")
	}
}

func TestItemFilterValue(t *testing.T) {
	it := Item{ID: "a", Title: "vpc", Desc: "develop → ticket"}
	if !strings.Contains(it.FilterValue(), "vpc") || !strings.Contains(it.FilterValue(), "develop") {
		t.Fatalf("FilterValue = %q", it.FilterValue())
	}
}

func TestShellItemImplementsDefaultItem(t *testing.T) {
	it := shellItem{id: "start", title: "Start project", desc: "Create or resume"}
	if it.Title() != "Start project" || it.Description() != "Create or resume" {
		t.Fatalf("Title/Description = %q / %q", it.Title(), it.Description())
	}
	// bubbles DefaultDelegate requires Title+Description; blank methods → blank home menu.
	var listItem interface {
		FilterValue() string
		Title() string
		Description() string
	} = it
	_ = listItem
}

func TestIsCancelled(t *testing.T) {
	if !IsCancelled(ErrCancelled) {
		t.Fatal("ErrCancelled should match")
	}
	if IsCancelled(fmt.Errorf("other")) {
		t.Fatal("other error should not match")
	}
}
