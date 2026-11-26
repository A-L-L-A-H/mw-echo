package meshfleet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultGitTimeout = 20 * time.Second

// ErrPathEscape indicates a path traversal or containment violation outside the repository root.
var ErrPathEscape = errors.New("worktree path escapes repository containment")

// WorktreeInfo describes a single git worktree discovered via `git worktree list --porcelain`.
type WorktreeInfo struct {
	Path     string `json:"path"`
	Head     string `json:"head"`
	Branch   string `json:"branch"`
	Bare     bool   `json:"bare,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	Locked   bool   `json:"locked,omitempty"`
	Prunable bool   `json:"prunable,omitempty"`
}

// ListWorktrees returns all git worktrees registered in repoDir.
func ListWorktrees(ctx context.Context, repoDir string) ([]WorktreeInfo, error) {
	realRepoDir, err := validateRepoDir(repoDir)
	if err != nil {
		return nil, err
	}

	stdout, err := runGitCommand(ctx, realRepoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return parseWorktreePorcelain(stdout), nil
}

// CreateWorktree creates an isolated git worktree on a new branch inside repoDir.
func CreateWorktree(ctx context.Context, repoDir, branch, targetDir string) (WorktreeInfo, error) {
	realRepoDir, err := validateRepoDir(repoDir)
	if err != nil {
		return WorktreeInfo{}, err
	}

	cleanBranch, err := validateBranchName(branch)
	if err != nil {
		return WorktreeInfo{}, err
	}

	resolvedTarget, err := resolveContainedTargetDir(realRepoDir, targetDir)
	if err != nil {
		return WorktreeInfo{}, err
	}

	if _, statErr := os.Stat(resolvedTarget); statErr == nil {
		return WorktreeInfo{}, fmt.Errorf("%w: target worktree directory already exists: %s", ErrValidation, resolvedTarget)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return WorktreeInfo{}, statErr
	}

	if mkdirErr := os.MkdirAll(filepath.Dir(resolvedTarget), 0o755); mkdirErr != nil {
		return WorktreeInfo{}, fmt.Errorf("create worktree parent dir: %w", mkdirErr)
	}

	if _, gitErr := runGitCommand(ctx, realRepoDir, "worktree", "add", "-b", cleanBranch, "--", resolvedTarget); gitErr != nil {
		return WorktreeInfo{}, fmt.Errorf("git worktree add: %w", gitErr)
	}

	headOut, headErr := runGitCommand(ctx, resolvedTarget, "rev-parse", "HEAD")
	headSHA := ""
	if headErr == nil {
		headSHA = strings.TrimSpace(headOut)
	}

	canonicalTarget := resolvedTarget
	if evalPath, evalErr := filepath.EvalSymlinks(resolvedTarget); evalErr == nil {
		canonicalTarget = evalPath
	}

	return WorktreeInfo{
		Path:   canonicalTarget,
		Head:   headSHA,
		Branch: cleanBranch,
	}, nil
}

// RemoveWorktree removes an isolated git worktree strictly contained within repoDir.
func RemoveWorktree(ctx context.Context, repoDir, targetDir string) error {
	realRepoDir, err := validateRepoDir(repoDir)
	if err != nil {
		return err
	}

	resolvedTarget, err := resolveContainedTargetDir(realRepoDir, targetDir)
	if err != nil {
		return err
	}

	if _, gitErr := runGitCommand(ctx, realRepoDir, "worktree", "remove", "--force", "--", resolvedTarget); gitErr != nil {
		return fmt.Errorf("git worktree remove: %w", gitErr)
	}
	return nil
}

func validateRepoDir(rawRepoDir string) (string, error) {
	trimmed := strings.TrimSpace(rawRepoDir)
	if trimmed == "" {
		return "", fmt.Errorf("%w: repoDir is required", ErrValidation)
	}
	if strings.ContainsRune(trimmed, '\x00') {
		return "", fmt.Errorf("%w: repoDir contains NUL byte", ErrValidation)
	}
	if hasParentTraversal(trimmed) {
		return "", fmt.Errorf("%w: repoDir must not contain '..'", ErrPathEscape)
	}

	absRepo, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: invalid repoDir: %v", ErrValidation, err)
	}
	info, err := os.Stat(absRepo)
	if err != nil {
		return "", fmt.Errorf("%w: repoDir does not exist: %v", ErrValidation, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: repoDir is not a directory", ErrValidation)
	}

	realRepo, err := filepath.EvalSymlinks(absRepo)
	if err != nil {
		return "", fmt.Errorf("resolve repoDir symlinks: %w", err)
	}
	return realRepo, nil
}

func resolveContainedTargetDir(realRepoDir, rawTargetDir string) (string, error) {
	trimmed := strings.TrimSpace(rawTargetDir)
	if trimmed == "" {
		return "", fmt.Errorf("%w: targetDir is required", ErrValidation)
	}
	if strings.ContainsRune(trimmed, '\x00') {
		return "", fmt.Errorf("%w: targetDir contains NUL byte", ErrValidation)
	}
	if hasParentTraversal(trimmed) {
		return "", fmt.Errorf("%w: targetDir must not contain '..'", ErrPathEscape)
	}

	var candidate string
	if filepath.IsAbs(trimmed) {
		candidate = filepath.Clean(trimmed)
	} else {
		candidate = filepath.Clean(filepath.Join(realRepoDir, trimmed))
	}

	realCandidate, err := resolvePathWithExistingAncestor(candidate)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPathEscape, err)
	}

	rel, err := filepath.Rel(realRepoDir, realCandidate)
	if err != nil {
		return "", fmt.Errorf("%w: cannot relate targetDir to repoDir", ErrPathEscape)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: targetDir %q is outside repoDir %q", ErrPathEscape, realCandidate, realRepoDir)
	}
	return realCandidate, nil
}

func resolvePathWithExistingAncestor(target string) (string, error) {
	cleaned := filepath.Clean(target)
	if evalPath, err := filepath.EvalSymlinks(cleaned); err == nil {
		return evalPath, nil
	}

	var suffixParts []string
	cursor := cleaned
	for {
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return "", fmt.Errorf("no existing ancestor for %q", target)
		}
		suffixParts = append(suffixParts, filepath.Base(cursor))
		cursor = parent
		if evalAncestor, err := filepath.EvalSymlinks(cursor); err == nil {
			joined := evalAncestor
			for idx := len(suffixParts) - 1; idx >= 0; idx-- {
				joined = filepath.Join(joined, suffixParts[idx])
			}
			return joined, nil
		}
	}
}

func hasParentTraversal(rawPath string) bool {
	normalized := strings.ReplaceAll(rawPath, "\\", "/")
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func validateBranchName(rawBranch string) (string, error) {
	trimmed := strings.TrimSpace(rawBranch)
	if trimmed == "" {
		return "", fmt.Errorf("%w: branch is required", ErrValidation)
	}
	if strings.HasPrefix(trimmed, "-") ||
		strings.Contains(trimmed, "..") ||
		strings.Contains(trimmed, "@{") ||
		strings.Contains(trimmed, "//") ||
		strings.HasSuffix(trimmed, "/") ||
		strings.HasSuffix(trimmed, ".lock") {
		return "", fmt.Errorf("%w: invalid git branch name %q", ErrValidation, trimmed)
	}
	for _, ch := range trimmed {
		isSafe := (ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '.' || ch == '_' || ch == '-' || ch == '/'
		if !isSafe {
			return "", fmt.Errorf("%w: invalid character %q in branch name", ErrValidation, ch)
		}
	}
	return trimmed, nil
}

func runGitCommand(ctx context.Context, repoDir string, args ...string) (string, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultGitTimeout)
		defer cancel()
	}

	cmdArgs := append([]string{"-C", repoDir}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return "", errors.New(errMsg)
	}
	return stdoutBuf.String(), nil
}

func parseWorktreePorcelain(raw string) []WorktreeInfo {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	worktrees := make([]WorktreeInfo, 0)
	var current *WorktreeInfo

	flush := func() {
		if current != nil && current.Path != "" {
			worktrees = append(worktrees, *current)
		}
		current = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "worktree "):
			flush()
			current = &WorktreeInfo{
				Path: filepath.FromSlash(strings.TrimSpace(strings.TrimPrefix(trimmed, "worktree "))),
			}
		case current == nil:
			continue
		case strings.HasPrefix(trimmed, "HEAD "):
			current.Head = strings.TrimSpace(strings.TrimPrefix(trimmed, "HEAD "))
		case strings.HasPrefix(trimmed, "branch "):
			ref := strings.TrimSpace(strings.TrimPrefix(trimmed, "branch "))
			current.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case trimmed == "bare":
			current.Bare = true
		case trimmed == "detached":
			current.Detached = true
		case trimmed == "locked" || strings.HasPrefix(trimmed, "locked "):
			current.Locked = true
		case trimmed == "prunable" || strings.HasPrefix(trimmed, "prunable "):
			current.Prunable = true
		}
	}
	flush()
	return worktrees
}
