package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeProgramsSelectsEnabledDefault(t *testing.T) {
	cfg := Default()
	cursor := cfg.OpenWith[ProgramCursor]
	cursor.Enabled = false
	cfg.OpenWith[ProgramCursor] = cursor
	cfg.DefaultProgram = ProgramCursor
	cfg.NormalizePrograms()
	if cfg.DefaultProgram != ProgramGoland {
		t.Fatalf("DefaultProgram = %q, want %q", cfg.DefaultProgram, ProgramGoland)
	}

	goland := cfg.OpenWith[ProgramGoland]
	goland.Enabled = false
	cfg.OpenWith[ProgramGoland] = goland
	cfg.NormalizePrograms()
	if cfg.DefaultProgram != "" {
		t.Fatalf("DefaultProgram = %q, want empty when all programs are disabled", cfg.DefaultProgram)
	}
}

func TestLoadMigratesLegacyProgramSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"cursor_path":"cursor","goland_path":"goland","cursor_enabled":false,"goland_enabled":true,"default_program":"goland"}`)
	if err := os.WriteFile(filepath.Join(dir, fileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cursor, ok := cfg.Program(ProgramCursor)
	if !ok || cursor.Enabled {
		t.Fatalf("migrated Cursor = %+v, exists=%t", cursor, ok)
	}
	goland, ok := cfg.Program(ProgramGoland)
	if !ok || !goland.Enabled || cfg.DefaultProgram != ProgramGoland {
		t.Fatalf("migrated GoLand/default = %+v, default=%q", goland, cfg.DefaultProgram)
	}
}

func TestValidateProgramIDAndPath(t *testing.T) {
	if !ValidProgramID("my-editor_2") || ValidProgramID("Bad ID") || ValidProgramID("-bad") {
		t.Fatal("unexpected program ID validation")
	}
	if err := ValidateProgramPath(os.Args[0]); err != nil {
		t.Fatalf("ValidateProgramPath executable: %v", err)
	}
	if err := ValidateProgramPath(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing program path accepted")
	}
}
