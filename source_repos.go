package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pershin-daniil/goworktree/internal/config"
	gitops "github.com/pershin-daniil/goworktree/internal/git"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
	"github.com/pershin-daniil/goworktree/internal/workflow/sourcerepos"
)

type sourceRepositoryScanResult struct {
	Added    int
	Migrated int
	Total    int
	Depth    int
}

func (r sourceRepositoryScanResult) String() string {
	return fmt.Sprintf("%d new, %d migrated, %d total (depth=%d)", r.Added, r.Migrated, r.Total, r.Depth)
}

func configuredSourceRepositoryConfigs(cfg *config.Config, ids []string) ([]sourcerepos.RepositoryConfig, error) {
	if err := uniqueIDs(ids); err != nil {
		return nil, err
	}
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	result := make([]sourcerepos.RepositoryConfig, 0, len(ids))
	for _, id := range ids {
		repository, ok := cfg.Repos[id]
		if !ok {
			return nil, fmt.Errorf("repository %q is not configured", id)
		}
		result = append(result, sourcerepos.RepositoryConfig{
			ID: id, Name: cfg.DisplayName(id), Path: repository.Path, Remote: cfg.RepoRemote(id),
			DefaultBranch: cfg.RepoBranch(id), Groups: cfg.RepoGroups(id),
		})
	}
	return result, nil
}

func requirePathWithinReposRoot(root, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repositories root: %w", err)
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve repository path: %w", err)
	}
	if !pathContained(rootAbs, pathAbs) {
		return fmt.Errorf("path %s is outside repositories root %s", pathAbs, rootAbs)
	}
	canonicalRoot, rootErr := filepath.EvalSymlinks(rootAbs)
	canonicalPath, pathErr := filepath.EvalSymlinks(pathAbs)
	if rootErr == nil && pathErr == nil && !pathContained(canonicalRoot, canonicalPath) {
		return fmt.Errorf("canonical path %s is outside repositories root %s", canonicalPath, canonicalRoot)
	}
	return nil
}

func pathContained(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func inspectConfiguredSourceRepositories(ctx context.Context, ids []string) (sourcerepos.Snapshot, error) {
	cfg, err := config.Load()
	if err != nil {
		return sourcerepos.Snapshot{}, err
	}
	configs, err := configuredSourceRepositoryConfigs(cfg, ids)
	if err != nil {
		return sourcerepos.Snapshot{}, err
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	return (sourcerepos.Inspector{Git: sourcerepos.SystemGit{}}).Inspect(operationCtx, configs), nil
}

func inspectAllConfiguredSourceRepositories(ctx context.Context) (sourcerepos.Snapshot, error) {
	cfg, err := config.Load()
	if err != nil {
		return sourcerepos.Snapshot{}, err
	}
	configs, err := configuredSourceRepositoryConfigs(cfg, cfg.RepoNames())
	if err != nil {
		return sourcerepos.Snapshot{}, err
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	return (sourcerepos.Inspector{Git: sourcerepos.SystemGit{}}).Inspect(operationCtx, configs), nil
}

func fetchConfiguredSourceRepositories(ctx context.Context, ids []string) (sourcerepos.Result, error) {
	cfg, err := config.Load()
	if err != nil {
		return sourcerepos.Result{}, err
	}
	configs, err := configuredSourceRepositoryConfigs(cfg, ids)
	if err != nil {
		return sourcerepos.Result{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return sourcerepos.Result{}, fmt.Errorf("resolve control root: %w", err)
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	return (sourcerepos.Fetcher{
		Git: sourcerepos.SystemGit{}, Locker: sourcerepos.FileLocker{Set: lockops.Set{Root: controlRoot}},
	}).Fetch(operationCtx, configs)
}

func planConfiguredSourceRepositoryUpdate(ctx context.Context, ids []string) (sourcerepos.Plan, error) {
	cfg, err := config.Load()
	if err != nil {
		return sourcerepos.Plan{}, err
	}
	configs, err := configuredSourceRepositoryConfigs(cfg, ids)
	if err != nil {
		return sourcerepos.Plan{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return sourcerepos.Plan{}, fmt.Errorf("resolve control root: %w", err)
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	return (sourcerepos.Planner{
		Git: sourcerepos.SystemGit{}, Locker: sourcerepos.FileLocker{Set: lockops.Set{Root: controlRoot}},
	}).Build(operationCtx, configs)
}

func runConfiguredSourceRepositoryUpdate(ctx context.Context, plan sourcerepos.Plan) (sourcerepos.Result, error) {
	cfg, err := config.Load()
	if err != nil {
		return sourcerepos.Result{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return sourcerepos.Result{}, fmt.Errorf("resolve control root: %w", err)
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	return (sourcerepos.Executor{
		Git: sourcerepos.SystemGit{}, Locker: sourcerepos.FileLocker{Set: lockops.Set{Root: controlRoot}},
	}).Execute(operationCtx, plan)
}

func openConfiguredSourceRepository(ctx context.Context, repositoryID, programID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	path, ok := cfg.RepoPath(repositoryID)
	if !ok {
		return fmt.Errorf("repository %q is not configured", repositoryID)
	}
	if programID == "" {
		programID = cfg.DefaultProgram
	}
	return openProgram(cfg, programID, path)
}

func addConfiguredSourceRepositoryGroup(ctx context.Context, ids []string, group string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.AddRepoGroup(ids, group); err != nil {
		return err
	}
	return cfg.Save()
}

func removeConfiguredSourceRepositoryGroup(ctx context.Context, ids []string, group string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.RemoveRepoGroup(ids, group); err != nil {
		return err
	}
	return cfg.Save()
}

func scanConfiguredSourceRepositories(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	scanned, err := gitops.ScanRepos(cfg.ReposRoot, cfg.ScanDepth)
	if err != nil {
		return "", err
	}
	result := sourceRepositoryScanResult{Depth: cfg.ScanDepth}
	for _, repository := range scanned {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		id := config.RepoIDFromPath(cfg.ReposRoot, repository.Path)
		existingID := id
		for otherID, other := range cfg.Repos {
			if other.Path == repository.Path {
				existingID = otherID
				break
			}
		}
		if existing, exists := cfg.Repos[existingID]; exists {
			existing.Path = repository.Path
			if existing.Alias == "" {
				existing.Alias = repository.Alias
			}
			cfg.Repos[existingID] = existing
			if existingID != id {
				if _, collision := cfg.Repos[id]; collision {
					return "", fmt.Errorf("scan repository %s: stable ID %q already exists", repository.Path, id)
				}
				cfg.Repos[id] = cfg.Repos[existingID]
				delete(cfg.Repos, existingID)
				result.Migrated++
			}
			continue
		}
		branch, branchErr := gitops.DefaultBranch(repository.Path)
		if branchErr != nil {
			branch = cfg.DefaultBranch
		}
		cfg.Repos[id] = config.Repo{Path: repository.Path, DefaultBranch: branch, Alias: repository.Alias}
		result.Added++
	}
	if err := cfg.Save(); err != nil {
		return "", err
	}
	result.Total = len(cfg.Repos)
	return result.String(), nil
}

func sourceRepositoryScope(cfg *config.Config, args []string, requireExplicit bool) ([]string, error) {
	ids, group, all, err := parseSourceRepositoryScope(args)
	if err != nil {
		return nil, err
	}
	modes := 0
	if len(ids) > 0 {
		modes++
	}
	if group != "" {
		modes++
	}
	if all {
		modes++
	}
	if modes > 1 {
		return nil, fmt.Errorf("select exactly one scope: --repos, --group, or --all")
	}
	if modes == 0 {
		if requireExplicit {
			return nil, fmt.Errorf("select repository scope with --repos, --group, or --all")
		}
		ids = cfg.RepoNames()
	}
	if group != "" {
		if !config.ValidGroupName(group) {
			return nil, fmt.Errorf("invalid repository group %q", group)
		}
		for _, id := range cfg.RepoNames() {
			if containsString(cfg.RepoGroups(id), group) {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("repository group %q does not exist or is empty", group)
		}
	}
	if all {
		ids = cfg.RepoNames()
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("repository scope is empty")
	}
	if err := uniqueIDs(ids); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, ok := cfg.Repos[id]; !ok {
			return nil, fmt.Errorf("repository %q is not configured", id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func parseSourceRepositoryScope(args []string) (ids []string, group string, all bool, err error) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--all":
			all = true
		case arg == "--repos":
			if index+1 >= len(args) {
				return nil, "", false, fmt.Errorf("--repos requires a comma-separated value")
			}
			index++
			ids = splitCSV(args[index])
		case strings.HasPrefix(arg, "--repos="):
			ids = splitCSV(strings.TrimPrefix(arg, "--repos="))
		case arg == "--group":
			if index+1 >= len(args) {
				return nil, "", false, fmt.Errorf("--group requires a value")
			}
			index++
			group = strings.TrimSpace(args[index])
		case strings.HasPrefix(arg, "--group="):
			group = strings.TrimSpace(strings.TrimPrefix(arg, "--group="))
		default:
			return nil, "", false, fmt.Errorf("unknown repository scope argument %q", arg)
		}
	}
	return ids, group, all, nil
}

func splitCSV(value string) []string {
	var result []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func runReposList(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.Repos) == 0 && len(args) == 0 {
		return nil
	}
	ids, err := sourceRepositoryScope(cfg, args, false)
	if err != nil {
		return err
	}
	for _, id := range ids {
		groups := cfg.RepoGroups(id)
		groupText := "-"
		if len(groups) > 0 {
			groupText = strings.Join(groups, ",")
		}
		path, _ := cfg.RepoPath(id)
		fmt.Printf("%-24s %-16s %-20s %s\n", id, cfg.RepoBranch(id), groupText, path)
	}
	return nil
}

func runReposStatus(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ids, err := sourceRepositoryScope(cfg, args, false)
	if err != nil {
		return err
	}
	snapshot, err := inspectConfiguredSourceRepositories(context.Background(), ids)
	if err != nil {
		return err
	}
	failed := false
	for _, repository := range snapshot.Repositories {
		label := sourceRelationLabel(repository.Relation, false)
		if repository.Problem != "" {
			label, failed = "failed: "+repository.Problem, true
		}
		fmt.Printf("%-24s %-18s %s\n", repository.ID, label, repository.Path)
	}
	if failed {
		return fmt.Errorf("one or more source repositories could not be inspected")
	}
	return nil
}

func runReposFetch(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ids, err := sourceRepositoryScope(cfg, args, true)
	if err != nil {
		return err
	}
	result, err := fetchConfiguredSourceRepositories(context.Background(), ids)
	if err != nil {
		return err
	}
	for _, repository := range result.Repositories {
		line := sourceRelationLabel(repository.Relation, true)
		if repository.Err != nil {
			line = repository.Err.Error()
		}
		fmt.Printf("  %-10s %-24s %s\n", repository.Status, repository.ID, line)
	}
	if result.NeedsAttention() {
		return fmt.Errorf("one or more source repositories could not be fetched")
	}
	return nil
}

func runReposUpdate(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ids, err := sourceRepositoryScope(cfg, args, true)
	if err != nil {
		return err
	}
	plan, err := planConfiguredSourceRepositoryUpdate(context.Background(), ids)
	if err != nil {
		return err
	}
	fmt.Printf("updating %d source repositories from fetched immutable commits\n", len(plan.Repositories))
	for _, repository := range plan.Repositories {
		detail := repository.Reason
		if detail == "" && repository.TargetOID != "" {
			detail = shortCLIRevision(repository.LocalOID) + " -> " + shortCLIRevision(repository.TargetOID)
		}
		fmt.Printf("  %-18s %-24s %s\n", repository.Action, repository.ID, detail)
	}
	result, err := runConfiguredSourceRepositoryUpdate(context.Background(), plan)
	if err != nil {
		return err
	}
	for _, repository := range result.Repositories {
		detail := ""
		if repository.Err != nil {
			detail = repository.Err.Error()
		}
		fmt.Printf("  %-10s %-24s %s\n", repository.Status, repository.ID, detail)
	}
	if result.NeedsAttention() {
		return fmt.Errorf("one or more source repositories need attention")
	}
	return nil
}

func runReposOpen(args []string) error {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("usage: goworktree repos open <id> [--program <id>]")
	}
	program := ""
	switch len(args) {
	case 1:
	case 2:
		if !strings.HasPrefix(args[1], "--program=") {
			return fmt.Errorf("usage: goworktree repos open <id> [--program <id>]")
		}
		program = strings.TrimSpace(strings.TrimPrefix(args[1], "--program="))
	case 3:
		if args[1] != "--program" {
			return fmt.Errorf("usage: goworktree repos open <id> [--program <id>]")
		}
		program = strings.TrimSpace(args[2])
	default:
		return fmt.Errorf("usage: goworktree repos open <id> [--program <id>]")
	}
	if len(args) > 1 && program == "" {
		return fmt.Errorf("usage: goworktree repos open <id> [--program <id>]")
	}
	return openConfiguredSourceRepository(context.Background(), args[0], program)
}

func runReposGroup(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: goworktree repos group <list|add|remove|rename|delete>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("usage: goworktree repos group list")
		}
		for _, group := range cfg.GroupNames() {
			count := 0
			for _, id := range cfg.RepoNames() {
				if containsString(cfg.RepoGroups(id), group) {
					count++
				}
			}
			fmt.Printf("%s\t%d\n", group, count)
		}
		return nil
	case "add", "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: goworktree repos group %s <group> --repos <ids>", args[0])
		}
		ids, scopeGroup, all, parseErr := parseSourceRepositoryScope(args[2:])
		if parseErr != nil {
			return parseErr
		}
		if len(ids) == 0 || scopeGroup != "" || all {
			return fmt.Errorf("usage: goworktree repos group %s <group> --repos <ids>", args[0])
		}
		if args[0] == "add" {
			err = cfg.AddRepoGroup(ids, args[1])
		} else {
			err = cfg.RemoveRepoGroup(ids, args[1])
		}
	case "rename":
		if len(args) != 3 {
			return fmt.Errorf("usage: goworktree repos group rename <old> <new>")
		}
		err = cfg.RenameGroup(args[1], args[2])
	case "delete":
		if len(args) != 2 {
			return fmt.Errorf("usage: goworktree repos group delete <group>")
		}
		err = cfg.DeleteGroup(args[1])
	default:
		return fmt.Errorf("unknown repos group subcommand: %s", args[0])
	}
	if err != nil {
		return err
	}
	return cfg.Save()
}

func sourceRelationLabel(relation sourcerepos.Relation, fetched bool) string {
	prefix := "known "
	if fetched {
		prefix = "fetched "
	}
	switch relation {
	case sourcerepos.RelationEqual:
		return prefix + "current"
	case sourcerepos.RelationBehind:
		return prefix + "behind"
	case sourcerepos.RelationAhead:
		return prefix + "ahead"
	case sourcerepos.RelationDiverged:
		return prefix + "diverged"
	case sourcerepos.RelationMissing:
		return "remote ?"
	default:
		return "unknown"
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
