package goland

import (
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
)

func OpenFolder(golandPath, folder string) error {
	abs, err := filepath.Abs(folder)
	if err != nil {
		return err
	}

	cmd := exec.Command(golandPath, abs)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open goland: %w", err)
	}
	return nil
}
