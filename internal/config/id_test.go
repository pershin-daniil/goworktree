package config

import (
	"path/filepath"
	"testing"
)

func TestRepoIDFromPath(t *testing.T) {
	root := "/Users/me/Projects"
	path := filepath.Join(root, "cloud", "vpc", "vpc-controller")
	got := RepoIDFromPath(root, path)
	want := "cloud-vpc-vpc-controller"
	if got != want {
		t.Fatalf("RepoIDFromPath = %q, want %q", got, want)
	}
}

func TestFolderNameUniqueAlias(t *testing.T) {
	cfg := &Config{
		Repos: map[string]Repo{
			"a-vpc": {Path: "/x/a/vpc", Alias: "vpc"},
			"b-api": {Path: "/x/b/api", Alias: "api"},
		},
	}
	selected := []string{"a-vpc", "b-api"}
	if got := cfg.FolderName("a-vpc", selected); got != "vpc" {
		t.Fatalf("unique FolderName = %q", got)
	}
}

func TestFolderNameCollisionUsesID(t *testing.T) {
	cfg := &Config{
		Repos: map[string]Repo{
			"cloud-a-vpc": {Path: "/x/a/vpc", Alias: "vpc"},
			"cloud-b-vpc": {Path: "/x/b/vpc", Alias: "vpc"},
		},
	}
	selected := []string{"cloud-a-vpc", "cloud-b-vpc"}
	if got := cfg.FolderName("cloud-a-vpc", selected); got != "cloud-a-vpc" {
		t.Fatalf("collision FolderName = %q", got)
	}
}
