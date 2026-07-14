package config

import (
	"os"
	"os/exec"
	"path/filepath"
)

func DefaultGolandPath() string {
	candidates := []string{
		"/Applications/GoLand.app/Contents/MacOS/goland",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if path, err := exec.LookPath("goland"); err == nil {
		return path
	}
	return "goland"
}

func DefaultCursorPath() string {
	candidates := []string{
		"/Applications/Cursor.app/Contents/MacOS/Cursor",
		"/Applications/Cursor.app/Contents/Resources/app/bin/cursor",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if path, err := exec.LookPath("cursor"); err == nil {
		return path
	}
	return "cursor"
}

func defaultReposRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "~/Projects"
	}
	for _, candidate := range []string{
		filepath.Join(home, "Projects", "GitHub"),
		filepath.Join(home, "Projects"),
	} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return filepath.Join(home, "Projects")
}

func defaultProjectsRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "~/Projects/worktrees"
	}
	return filepath.Join(home, "Projects", "worktrees")
}
