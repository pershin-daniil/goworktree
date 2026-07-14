package cursor

import (
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
)

func Open(cursorPath string, paths ...string) error {
	if len(paths) == 0 {
		return fmt.Errorf("no paths to open")
	}

	args := make([]string, 0, len(paths)+1)
	args = append(args, paths...)

	cmd := exec.Command(cursorPath, args...)
	// Cursor (Electron/Node) prints deprecation warnings to stderr; if those hit
	// the terminal while a Bubble Tea alt-screen is up, the menu corrupts.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open cursor: %w", err)
	}
	return nil
}

func OpenFolder(cursorPath, folder string) error {
	abs, err := filepath.Abs(folder)
	if err != nil {
		return err
	}
	return Open(cursorPath, abs)
}
