package newwork

import "testing"

func TestParseGoVersionOrderingAndCanonicalForm(t *testing.T) {
	t.Parallel()

	v123, err := parseGoVersion("1.23")
	if err != nil {
		t.Fatal(err)
	}
	v1231, err := parseGoVersion("1.23.1")
	if err != nil {
		t.Fatal(err)
	}
	v124, err := parseGoVersion("1.24.0")
	if err != nil {
		t.Fatal(err)
	}
	if v1231.compare(v123) <= 0 || v124.compare(v1231) <= 0 {
		t.Fatalf("version ordering is wrong: %v %v %v", v123, v1231, v124)
	}
	if got := v124.String(); got != "1.24" {
		t.Fatalf("canonical version = %q, want 1.24", got)
	}
	for _, invalid := range []string{"", "1", "2.0", "1.x", "1.2.3.4", "1.23rc1", "1.023"} {
		if _, err := parseGoVersion(invalid); err == nil {
			t.Fatalf("parseGoVersion(%q) succeeded", invalid)
		}
	}
}

func TestStripGoModComments(t *testing.T) {
	t.Parallel()

	inBlock := false
	if got := stripGoModComments("/* before", &inBlock); got != "" || !inBlock {
		t.Fatalf("block start = %q, %v", got, inBlock)
	}
	if got := stripGoModComments("after */ go 1.24 // trailing", &inBlock); got != " go 1.24 " || inBlock {
		t.Fatalf("block end = %q, %v", got, inBlock)
	}
}
