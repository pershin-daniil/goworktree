package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pershin-daniil/goworktree/internal/config"
)

func TestOpenProgramReportsLauncherExitError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test launcher is a POSIX shell script")
	}

	dir := t.TempDir()
	launcher := filepath.Join(dir, "open")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\necho 'application not found' >&2\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{OpenWith: map[string]config.Program{
		"broken": {Name: "Broken", Path: launcher, Enabled: true},
	}}

	err := openProgram(cfg, "broken", dir)
	if err == nil || !strings.Contains(err.Error(), "exit status 7") || !strings.Contains(err.Error(), "application not found") {
		t.Fatalf("openProgram() error = %v", err)
	}
}

func TestWaitForLauncher(t *testing.T) {
	tests := []struct {
		program config.Program
		want    bool
	}{
		{program: config.Program{Path: "/usr/bin/open"}, want: true},
		{program: config.Program{Path: "/usr/bin/xdg-open"}, want: true},
		{program: config.Program{Path: "/usr/local/bin/codex", Args: []string{"app"}}, want: true},
		{program: config.Program{Path: "/usr/local/bin/codex"}, want: false},
		{program: config.Program{Path: "/Applications/GoLand.app/Contents/MacOS/goland"}, want: false},
	}

	for _, tt := range tests {
		if got := waitForLauncher(tt.program); got != tt.want {
			t.Errorf("waitForLauncher(%+v) = %v, want %v", tt.program, got, tt.want)
		}
	}
}
