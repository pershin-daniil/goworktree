package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pershin-daniil/goworktree/internal/config"
	"github.com/pershin-daniil/goworktree/internal/project"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/changework"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/syncwork"
)

func runBranch(args []string) error {
	if len(args) != 2 || args[0] == "" || args[1] == "" {
		return fmt.Errorf("usage: goworktree branch <work> <repo>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	root, err := project.Dir(cfg, args[0])
	if err != nil {
		return err
	}
	data, err := work.ReadRegularFile(filepath.Join(root, project.ManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return runLegacyBranch(args)
	}
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("parse Work manifest: %w", err)
	}
	if _, typed := fields["schema_version"]; !typed {
		return runLegacyBranch(args)
	}
	ctx := context.Background()
	if resumed, err := resumePendingRepositoryChange(ctx, changework.ResumeRequest{
		WorkName: args[0], Kind: changework.KindAdopt, RepositoryIDs: []string{args[1]},
	}); resumed || err != nil {
		return err
	}
	cfg, catalog, err := configuredChangeCatalog(ctx, args[0], []string{args[1]}, changework.KindAdopt)
	if err != nil {
		return err
	}
	id, err := resolveWorkRepositoryID(catalog.Manifest, args[1])
	if err != nil {
		return err
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	plan, err := (changework.Planner{Git: changework.SystemGit{}}).BuildAdopt(operationCtx, catalog, changework.AdoptRequest{
		WorkName: args[0], RepositoryID: id,
	})
	if err != nil {
		return err
	}
	entry := plan.Repositories[0]
	for _, before := range plan.Before.Repositories {
		if before.ID == id && before.BranchRef == entry.BranchRef {
			fmt.Printf("%s already uses branch %q\n", id, strings.TrimPrefix(entry.BranchRef, "refs/heads/"))
			return nil
		}
	}
	result, err := runConfiguredRepositoryChange(operationCtx, plan)
	if err != nil {
		return err
	}
	fmt.Printf("%s: adopted branch %q at %s (revision %d)\n", id,
		strings.TrimPrefix(entry.BranchRef, "refs/heads/"), shortCLIRevision(entry.BranchOID), result.Revision)
	return nil
}

func resolveWorkRepositoryID(manifest work.Manifest, value string) (string, error) {
	id := ""
	for _, repository := range manifest.Repositories {
		if repository.ID == value || filepath.Base(repository.Destination) == value {
			if id != "" {
				return "", fmt.Errorf("repository %q is ambiguous; use its manifest ID", value)
			}
			id = repository.ID
		}
	}
	if id == "" {
		return "", fmt.Errorf("repository %q is not active in Work %q", value, manifest.Name)
	}
	return id, nil
}

func branchAdoptionProblem(code inspectwork.ProblemCode) bool {
	switch code {
	case inspectwork.ProblemBranchMissing, inspectwork.ProblemBranchRefMismatch,
		inspectwork.ProblemRegistrationConflict, inspectwork.ProblemHeadMismatch:
		return true
	default:
		return false
	}
}

func requireTerminalWorkOperations(controlRoot, workID string) error {
	syncPath := filepath.Join(controlRoot, "operations", "sync-work", workID+".json")
	if exists, terminal, err := syncwork.OperationTerminal(syncPath, workID); err != nil {
		return fmt.Errorf("inspect Sync Work before changing repositories: %w", err)
	} else if exists && !terminal {
		return fmt.Errorf("unfinished Sync Work blocks repository changes; finish Sync Work first")
	}
	removePath := filepath.Join(controlRoot, "operations", "remove-work", workID+".json")
	if _, err := os.Lstat(removePath); err == nil {
		return fmt.Errorf("unfinished Remove Work blocks repository changes; finish Remove Work first")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Remove Work before changing repositories: %w", err)
	}
	return nil
}
