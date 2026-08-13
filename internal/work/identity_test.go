package work

import "testing"

func TestNewIdentityIsDeterministicAndSeparatesRootAndName(t *testing.T) {
	t.Parallel()

	name, err := ParseName("work-1")
	if err != nil {
		t.Fatal(err)
	}
	first := NewIdentity("/works", name)
	if first == "" || first != NewIdentity("/works", name) {
		t.Fatalf("identity is not stable: %q", first)
	}
	if first == NewIdentity("/other-works", name) {
		t.Fatal("different works roots share identity")
	}
	other, err := ParseName("work-2")
	if err != nil {
		t.Fatal(err)
	}
	if first == NewIdentity("/works", other) {
		t.Fatal("different Work names share identity")
	}
}
