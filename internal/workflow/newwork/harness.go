package newwork

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pershin-daniil/goworktree/internal/work"
)

const minimumGoWorkVersion = "1.23"

type HarnessManager interface {
	Ensure(Plan) (HarnessCheckpoint, error)
	Verify(Plan, HarnessCheckpoint) error
}

type GoWorkHarness struct{}

func (GoWorkHarness) Ensure(plan Plan) (HarnessCheckpoint, error) {
	expected, checkpoint, err := expectedGoWork(plan)
	if err != nil {
		return HarnessCheckpoint{}, err
	}
	path := filepath.Join(plan.WorkRoot, "go.work")
	if err := work.CleanupAtomicTemps(path); err != nil {
		return HarnessCheckpoint{}, fmt.Errorf("clean go.work temporary files: %w", err)
	}
	if !checkpoint.Generated {
		if _, err := os.Lstat(path); err == nil {
			return HarnessCheckpoint{}, fmt.Errorf("go.work exists although the plan contains no root modules")
		} else if !errors.Is(err, os.ErrNotExist) {
			return HarnessCheckpoint{}, err
		}
		return checkpoint, nil
	}
	current, err := work.ReadRegularFile(path)
	if err == nil {
		if !bytes.Equal(current, expected) {
			return HarnessCheckpoint{}, fmt.Errorf("existing go.work does not match planned harness")
		}
		return checkpoint, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return HarnessCheckpoint{}, err
	}
	if err := work.CreateFile(path, expected, 0o644); err != nil {
		return HarnessCheckpoint{}, err
	}
	current, err = work.ReadRegularFile(path)
	if err != nil {
		return HarnessCheckpoint{}, err
	}
	if !bytes.Equal(current, expected) {
		return HarnessCheckpoint{}, fmt.Errorf("written go.work does not match planned harness")
	}
	return checkpoint, nil
}

func (GoWorkHarness) Verify(plan Plan, checkpoint HarnessCheckpoint) error {
	if checkpoint.State != StepVerified {
		return fmt.Errorf("harness is not checkpointed as verified")
	}
	expected, currentCheckpoint, err := expectedGoWork(plan)
	if err != nil {
		return err
	}
	if checkpoint.Generated != currentCheckpoint.Generated || checkpoint.GoVersion != currentCheckpoint.GoVersion ||
		!equalStrings(checkpoint.UsePaths, currentCheckpoint.UsePaths) {
		return fmt.Errorf("harness checkpoint does not match planned root modules")
	}
	path := filepath.Join(plan.WorkRoot, "go.work")
	if !checkpoint.Generated {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		return fmt.Errorf("unexpected go.work exists")
	}
	actual, err := work.ReadRegularFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("go.work content does not match verified harness")
	}
	return nil
}

func expectedGoWork(plan Plan) ([]byte, HarnessCheckpoint, error) {
	version, err := parseGoVersion(minimumGoWorkVersion)
	if err != nil {
		return nil, HarnessCheckpoint{}, err
	}
	var usePaths []string
	for _, repo := range plan.Repositories {
		if !repo.IncludeInGoWork {
			continue
		}
		modulePath := filepath.Join(repo.Destination, "go.mod")
		info, err := os.Lstat(modulePath)
		if err != nil {
			return nil, HarnessCheckpoint{}, fmt.Errorf("repository %q root go.mod: %w", repo.ID, err)
		}
		if !info.Mode().IsRegular() {
			return nil, HarnessCheckpoint{}, fmt.Errorf("repository %q root go.mod is not a regular file", repo.ID)
		}
		moduleVersion, err := readModuleGoVersion(modulePath)
		if err != nil {
			return nil, HarnessCheckpoint{}, fmt.Errorf("repository %q: %w", repo.ID, err)
		}
		if moduleVersion.compare(version) > 0 {
			version = moduleVersion
		}
		relative, err := filepath.Rel(plan.WorkRoot, repo.Destination)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, HarnessCheckpoint{}, fmt.Errorf("repository %q destination is outside Work root", repo.ID)
		}
		usePaths = append(usePaths, "./"+filepath.ToSlash(relative))
	}
	checkpoint := HarnessCheckpoint{Generated: len(usePaths) > 0, UsePaths: usePaths}
	if len(usePaths) == 0 {
		return nil, checkpoint, nil
	}
	checkpoint.GoVersion = version.String()
	var content strings.Builder
	fmt.Fprintf(&content, "go %s\n\nuse (\n", checkpoint.GoVersion)
	for _, path := range usePaths {
		fmt.Fprintf(&content, "\t%s\n", path)
	}
	content.WriteString(")\n")
	return []byte(content.String()), checkpoint, nil
}

func readModuleGoVersion(path string) (goVersion, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return goVersion{}, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	inBlockComment := false
	for scanner.Scan() {
		line := stripGoModComments(scanner.Text(), &inBlockComment)
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "go" {
			if len(fields) != 2 {
				return goVersion{}, fmt.Errorf("invalid go directive in %s", path)
			}
			return parseGoVersion(fields[1])
		}
	}
	if err := scanner.Err(); err != nil {
		return goVersion{}, err
	}
	if inBlockComment {
		return goVersion{}, fmt.Errorf("unterminated block comment in %s", path)
	}
	return parseGoVersion(minimumGoWorkVersion)
}

type goVersion struct {
	major int
	minor int
	patch int
}

func parseGoVersion(value string) (goVersion, error) {
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return goVersion{}, fmt.Errorf("invalid Go version %q", value)
	}
	numbers := make([]int, 3)
	for i, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return goVersion{}, fmt.Errorf("invalid Go version %q", value)
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return goVersion{}, fmt.Errorf("invalid Go version %q", value)
		}
		numbers[i] = n
	}
	if numbers[0] != 1 {
		return goVersion{}, fmt.Errorf("unsupported Go major version %q", value)
	}
	return goVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}, nil
}

func (v goVersion) compare(other goVersion) int {
	left := []int{v.major, v.minor, v.patch}
	right := []int{other.major, other.minor, other.patch}
	for i := range left {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}

func (v goVersion) String() string {
	if v.patch == 0 {
		return fmt.Sprintf("%d.%d", v.major, v.minor)
	}
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stripGoModComments(line string, inBlock *bool) string {
	var result strings.Builder
	for i := 0; i < len(line); {
		if *inBlock {
			end := strings.Index(line[i:], "*/")
			if end < 0 {
				return result.String()
			}
			i += end + 2
			*inBlock = false
			continue
		}
		if strings.HasPrefix(line[i:], "//") {
			break
		}
		if strings.HasPrefix(line[i:], "/*") {
			*inBlock = true
			i += 2
			continue
		}
		result.WriteByte(line[i])
		i++
	}
	return result.String()
}
