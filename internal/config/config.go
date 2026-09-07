package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const fileName = "config.json"
const DefaultScanDepth = 3
const DefaultCommandTimeoutSeconds = 300

const (
	ProgramCursor = "cursor"
	ProgramGoland = "goland"
)

type Program struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Args    []string `json:"args,omitempty"`
	Enabled bool     `json:"enabled"`
}

type Config struct {
	OpenWith              map[string]Program `json:"open_with"`
	DefaultProgram        string             `json:"default_program"`
	ConflictProgram       string             `json:"conflict_program,omitempty"`
	ReposRoot             string             `json:"repos_root"`
	ProjectsRoot          string             `json:"projects_root"`
	DefaultBranch         string             `json:"default_branch"`
	ScanDepth             int                `json:"scan_depth,omitempty"`
	CommandTimeoutSeconds int                `json:"command_timeout_seconds,omitempty"`
	Repos                 map[string]Repo    `json:"repos,omitempty"`
}

type Repo struct {
	Path          string   `json:"path,omitempty"`
	DefaultBranch string   `json:"default_branch,omitempty"`
	Remote        string   `json:"remote,omitempty"`
	Alias         string   `json:"alias,omitempty"`
	Groups        []string `json:"groups,omitempty"`
}

func Default() *Config {
	return &Config{
		OpenWith: map[string]Program{
			ProgramCursor: {Name: "Cursor", Path: DefaultCursorPath(), Enabled: true},
			ProgramGoland: {Name: "GoLand", Path: DefaultGolandPath(), Enabled: true},
		},
		DefaultProgram:        ProgramCursor,
		ReposRoot:             defaultReposRoot(),
		ProjectsRoot:          defaultProjectsRoot(),
		DefaultBranch:         "main",
		ScanDepth:             DefaultScanDepth,
		CommandTimeoutSeconds: DefaultCommandTimeoutSeconds,
		Repos:                 map[string]Repo{},
	}
}

func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "goworktree"), nil
}

func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("config not found — run `goworktree init`")
	}
	if err != nil {
		return nil, err
	}

	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if _, ok := fields["open_with"]; !ok {
		legacy := struct {
			CursorPath    string `json:"cursor_path"`
			GolandPath    string `json:"goland_path"`
			CursorEnabled bool   `json:"cursor_enabled"`
			GolandEnabled bool   `json:"goland_enabled"`
		}{CursorEnabled: true, GolandEnabled: true}
		if err := json.Unmarshal(data, &legacy); err != nil {
			return nil, fmt.Errorf("parse legacy programs: %w", err)
		}
		if legacy.CursorPath == "" {
			legacy.CursorPath = DefaultCursorPath()
		}
		if legacy.GolandPath == "" {
			legacy.GolandPath = DefaultGolandPath()
		}
		cfg.OpenWith = map[string]Program{
			ProgramCursor: {Name: "Cursor", Path: legacy.CursorPath, Enabled: legacy.CursorEnabled},
			ProgramGoland: {Name: "GoLand", Path: legacy.GolandPath, Enabled: legacy.GolandEnabled},
		}
	}
	if cfg.OpenWith == nil {
		cfg.OpenWith = map[string]Program{}
	}
	cfg.expandPaths()
	if cfg.Repos == nil {
		cfg.Repos = map[string]Repo{}
	}
	if err := cfg.normalizeRepoGroups(); err != nil {
		return nil, fmt.Errorf("validate repository groups: %w", err)
	}
	if cfg.ScanDepth <= 0 {
		cfg.ScanDepth = DefaultScanDepth
	}
	if cfg.CommandTimeoutSeconds <= 0 {
		cfg.CommandTimeoutSeconds = DefaultCommandTimeoutSeconds
	}
	cfg.NormalizePrograms()
	return cfg, nil
}

func (c *Config) Save() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	c.expandPaths()
	if c.ScanDepth <= 0 {
		c.ScanDepth = DefaultScanDepth
	}
	if c.CommandTimeoutSeconds <= 0 {
		c.CommandTimeoutSeconds = DefaultCommandTimeoutSeconds
	}
	c.NormalizePrograms()
	if err := c.normalizeRepoGroups(); err != nil {
		return fmt.Errorf("validate repository groups: %w", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	path := filepath.Join(dir, fileName)
	return atomicWriteFile(path, data, 0o644)
}

// EnabledPrograms returns program identifiers in stable ID order.
func (c *Config) EnabledPrograms() []string {
	programs := make([]string, 0, len(c.OpenWith))
	for id, program := range c.OpenWith {
		if program.Enabled {
			programs = append(programs, id)
		}
	}
	sort.Strings(programs)
	return programs
}

func (c *Config) ProgramEnabled(program string) bool {
	p, ok := c.OpenWith[program]
	return ok && p.Enabled
}

// EffectiveConflictProgram returns the explicitly configured conflict
// resolver when it is enabled, otherwise conflicts follow the default program.
func (c *Config) EffectiveConflictProgram() string {
	if c.ProgramEnabled(c.ConflictProgram) {
		return c.ConflictProgram
	}
	return c.DefaultProgram
}

func (c *Config) Program(id string) (Program, bool) { p, ok := c.OpenWith[id]; return p, ok }

func (c *Config) ProgramIDs() []string {
	ids := make([]string, 0, len(c.OpenWith))
	for id := range c.OpenWith {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func ValidProgramID(id string) bool {
	if id == "" {
		return false
	}
	for i, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			if i == 0 && (r == '_' || r == '-') {
				return false
			}
			continue
		}
		return false
	}
	return true
}

// ValidGroupName reports whether name is a conservative portable group name.
// It accepts ASCII letters, digits, dots, underscores, and hyphens, and must
// begin with an ASCII letter or digit.
func ValidGroupName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		letter := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
		digit := ch >= '0' && ch <= '9'
		if i == 0 && !letter && !digit {
			return false
		}
		if !letter && !digit && ch != '.' && ch != '_' && ch != '-' {
			return false
		}
	}
	return true
}

func ValidateProgramPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("program path is required")
	}
	if strings.ContainsRune(path, os.PathSeparator) {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("program path %q: %w", path, err)
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			return fmt.Errorf("program path %q is not executable", path)
		}
		return nil
	}
	if _, err := exec.LookPath(path); err != nil {
		return fmt.Errorf("program command %q is not in PATH", path)
	}
	return nil
}

// NormalizePrograms keeps the selected default usable after a setting change.
// An empty default is permitted only when every program is disabled.
func (c *Config) NormalizePrograms() {
	if c.ConflictProgram != "" {
		if _, exists := c.OpenWith[c.ConflictProgram]; !exists {
			c.ConflictProgram = ""
		}
	}
	if c.ProgramEnabled(c.DefaultProgram) {
		return
	}
	programs := c.EnabledPrograms()
	if len(programs) == 0 {
		c.DefaultProgram = ""
		return
	}
	c.DefaultProgram = programs[0]
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".goworktree-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (c *Config) Exists() bool {
	path, err := Path()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

func (c *Config) RepoPath(id string) (string, bool) {
	if repo, ok := c.Repos[id]; ok && repo.Path != "" {
		return repo.Path, true
	}
	return "", false
}

// FindRepoID matches a primary clone path to a configured repo id.
func (c *Config) FindRepoID(mainPath string) (string, bool) {
	abs, err := filepath.Abs(mainPath)
	if err != nil {
		abs = filepath.Clean(mainPath)
	}
	for id, repo := range c.Repos {
		if repo.Path == "" {
			continue
		}
		rp, err := filepath.Abs(repo.Path)
		if err != nil {
			rp = filepath.Clean(repo.Path)
		}
		if rp == abs {
			return id, true
		}
	}
	base := filepath.Base(abs)
	var match string
	for id, repo := range c.Repos {
		alias := repo.Alias
		if alias == "" && repo.Path != "" {
			alias = filepath.Base(repo.Path)
		}
		if alias == base || filepath.Base(repo.Path) == base {
			if match != "" {
				return "", false // ambiguous
			}
			match = id
		}
	}
	if match != "" {
		return match, true
	}
	return "", false
}

func (c *Config) RepoBranch(id string) string {
	if repo, ok := c.Repos[id]; ok && repo.DefaultBranch != "" {
		return repo.DefaultBranch
	}
	return c.DefaultBranch
}

func (c *Config) RepoRemote(id string) string {
	if repo, ok := c.Repos[id]; ok && repo.Remote != "" {
		return repo.Remote
	}
	return "origin"
}

func (c *Config) RepoAlias(id string) string {
	if repo, ok := c.Repos[id]; ok && repo.Alias != "" {
		return repo.Alias
	}
	if repo, ok := c.Repos[id]; ok && repo.Path != "" {
		return filepath.Base(repo.Path)
	}
	return id
}

// RepoGroups returns a copy of the configured groups for id.
func (c *Config) RepoGroups(id string) []string {
	repo, ok := c.Repos[id]
	if !ok || len(repo.Groups) == 0 {
		return nil
	}
	return append([]string(nil), repo.Groups...)
}

// GroupNames returns all configured group names in lexical order.
func (c *Config) GroupNames() []string {
	groups := make(map[string]struct{})
	for _, repo := range c.Repos {
		for _, group := range repo.Groups {
			groups[group] = struct{}{}
		}
	}
	names := make([]string, 0, len(groups))
	for group := range groups {
		names = append(names, group)
	}
	sort.Strings(names)
	return names
}

// AddRepoGroup adds group to every specified repository.
func (c *Config) AddRepoGroup(ids []string, group string) error {
	if err := validateGroupName(group); err != nil {
		return err
	}
	if err := c.validateRepoGroups(); err != nil {
		return err
	}
	if err := c.requireRepoIDs(ids); err != nil {
		return err
	}
	for _, id := range ids {
		repo := c.Repos[id]
		repo.Groups = append(repo.Groups, group)
		c.Repos[id] = repo
	}
	return c.normalizeRepoGroups()
}

// RemoveRepoGroup removes group from every specified repository.
func (c *Config) RemoveRepoGroup(ids []string, group string) error {
	if err := validateGroupName(group); err != nil {
		return err
	}
	if err := c.validateRepoGroups(); err != nil {
		return err
	}
	if err := c.requireRepoIDs(ids); err != nil {
		return err
	}
	for _, id := range ids {
		repo := c.Repos[id]
		repo.Groups = removeGroup(repo.Groups, group)
		c.Repos[id] = repo
	}
	return c.normalizeRepoGroups()
}

// RenameGroup replaces old with new in every repository that uses old.
func (c *Config) RenameGroup(old, new string) error {
	if err := validateGroupName(old); err != nil {
		return err
	}
	if err := validateGroupName(new); err != nil {
		return err
	}
	if err := c.validateRepoGroups(); err != nil {
		return err
	}
	found := false
	for _, repo := range c.Repos {
		for _, group := range repo.Groups {
			if group == old {
				found = true
				break
			}
		}
	}
	if !found {
		return fmt.Errorf("repository group %q does not exist", old)
	}
	for id, repo := range c.Repos {
		for i, group := range repo.Groups {
			if group == old {
				repo.Groups[i] = new
			}
		}
		c.Repos[id] = repo
	}
	return c.normalizeRepoGroups()
}

// DeleteGroup removes group from every repository that uses it.
func (c *Config) DeleteGroup(group string) error {
	if err := validateGroupName(group); err != nil {
		return err
	}
	if err := c.validateRepoGroups(); err != nil {
		return err
	}
	found := false
	for _, repo := range c.Repos {
		for _, existing := range repo.Groups {
			if existing == group {
				found = true
				break
			}
		}
	}
	if !found {
		return fmt.Errorf("repository group %q does not exist", group)
	}
	for id, repo := range c.Repos {
		repo.Groups = removeGroup(repo.Groups, group)
		c.Repos[id] = repo
	}
	return c.normalizeRepoGroups()
}

func validateGroupName(group string) error {
	if !ValidGroupName(group) {
		return fmt.Errorf("invalid repository group %q", group)
	}
	return nil
}

func (c *Config) validateRepoGroups() error {
	for id, repo := range c.Repos {
		for _, group := range repo.Groups {
			if err := validateGroupName(group); err != nil {
				return fmt.Errorf("repository %q: %w", id, err)
			}
		}
	}
	return nil
}

func (c *Config) normalizeRepoGroups() error {
	if err := c.validateRepoGroups(); err != nil {
		return err
	}
	for id, repo := range c.Repos {
		repo.Groups = normalizeGroups(repo.Groups)
		c.Repos[id] = repo
	}
	return nil
}

func (c *Config) requireRepoIDs(ids []string) error {
	for _, id := range ids {
		if _, ok := c.Repos[id]; !ok {
			return fmt.Errorf("repository %q does not exist", id)
		}
	}
	return nil
}

func normalizeGroups(groups []string) []string {
	if len(groups) == 0 {
		return nil
	}
	unique := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		unique[group] = struct{}{}
	}
	normalized := make([]string, 0, len(unique))
	for group := range unique {
		normalized = append(normalized, group)
	}
	sort.Strings(normalized)
	return normalized
}

func removeGroup(groups []string, target string) []string {
	filtered := make([]string, 0, len(groups))
	for _, group := range groups {
		if group != target {
			filtered = append(filtered, group)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func (c *Config) RepoNames() []string {
	names := make([]string, 0, len(c.Repos))
	for name := range c.Repos {
		names = append(names, name)
	}
	return names
}

// DisplayName returns a human label for pickers: alias, or alias (rel) when ambiguous.
func (c *Config) DisplayName(id string) string {
	repo, ok := c.Repos[id]
	if !ok {
		return id
	}
	alias := repo.Alias
	if alias == "" && repo.Path != "" {
		alias = filepath.Base(repo.Path)
	}
	if alias == "" {
		return id
	}

	for otherID, other := range c.Repos {
		if otherID == id {
			continue
		}
		otherAlias := other.Alias
		if otherAlias == "" && other.Path != "" {
			otherAlias = filepath.Base(other.Path)
		}
		if otherAlias == alias {
			rel := id
			if c.ReposRoot != "" && strings.HasPrefix(repo.Path, c.ReposRoot) {
				if r, err := filepath.Rel(c.ReposRoot, repo.Path); err == nil {
					rel = r
				}
			}
			return fmt.Sprintf("%s (%s)", alias, rel)
		}
	}
	return alias
}

// FolderName returns the worktree directory name for id within a selected set.
// Uses alias when unique among selected; otherwise the full id.
func (c *Config) FolderName(id string, selected []string) string {
	alias := c.RepoAlias(id)
	count := 0
	for _, other := range selected {
		if c.RepoAlias(other) == alias {
			count++
		}
	}
	if count <= 1 {
		return alias
	}
	return id
}

func (c *Config) expandPaths() {
	c.ReposRoot = expandHome(c.ReposRoot)
	c.ProjectsRoot = expandHome(c.ProjectsRoot)

	for name, repo := range c.Repos {
		repo.Path = expandHome(repo.Path)
		c.Repos[name] = repo
	}
	for id, program := range c.OpenWith {
		program.Path = expandHome(program.Path)
		c.OpenWith[id] = program
	}
}

func expandHome(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// RepoIDFromPath builds a stable id from path relative to reposRoot.
func RepoIDFromPath(reposRoot, path string) string {
	rel := path
	if reposRoot != "" {
		if r, err := filepath.Rel(reposRoot, path); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}
	rel = filepath.ToSlash(rel)
	rel = strings.Trim(rel, "/")
	if rel == "" || rel == "." {
		return filepath.Base(path)
	}
	return strings.ReplaceAll(rel, "/", "-")
}
