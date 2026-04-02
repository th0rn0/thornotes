package notes

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/th0rn0/thornotes/internal/model"
)

// gitHistory wraps a go-git repository and provides the commit and log
// operations used by FileStore when git history is enabled.
type gitHistory struct {
	repo *gogit.Repository
	mu   sync.Mutex // serialises all index/commit operations
}

// openOrInitGitRepo opens the git repository at dir, initialising a new one
// (with a .gitignore for thornotes temp files) if none exists yet.
func openOrInitGitRepo(dir string) (*gitHistory, error) {
	repo, err := gogit.PlainOpen(dir)
	if errors.Is(err, gogit.ErrRepositoryNotExists) {
		repo, err = gogit.PlainInit(dir, false)
		if err != nil {
			return nil, fmt.Errorf("git init: %w", err)
		}
		if err := writeGitIgnore(dir); err != nil {
			return nil, err
		}
		if err := setGitIdentity(repo); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("git open: %w", err)
	}
	return &gitHistory{repo: repo}, nil
}

// writeGitIgnore creates a .gitignore at dir that excludes thornotes temp/probe files.
// It uses O_EXCL so it never overwrites an existing file.
func writeGitIgnore(dir string) error {
	path := dir + "/.gitignore"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return nil // already exists – leave it unchanged
	}
	if err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString(".thornotes-*.tmp\n.thornotes-probe-*\n")
	return err
}

// setGitIdentity writes a local user.name / user.email so commits don't fail
// when no global git config exists (common in containers).
func setGitIdentity(repo *gogit.Repository) error {
	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("git config read: %w", err)
	}
	cfg.User.Name = "thornotes"
	cfg.User.Email = "thornotes@localhost"
	if err := repo.SetConfig(cfg); err != nil {
		return fmt.Errorf("git config write: %w", err)
	}
	return nil
}

// CommitSave stages relativePath and records a "save:" commit.
func (g *gitHistory) CommitSave(relativePath string) error {
	return g.commitPath(relativePath, "save: "+relativePath)
}

// CommitDelete stages the removal of relativePath and records a "delete:" commit.
func (g *gitHistory) CommitDelete(relativePath string) error {
	return g.commitAll("delete: " + relativePath)
}

// CommitRename stages all changes (moves/deletes/creates from the rename) and
// records a "rename:" commit.
func (g *gitHistory) CommitRename(oldPath, newPath string) error {
	return g.commitAll("rename: " + oldPath + " -> " + newPath)
}

// commitPath stages a single path and commits.
func (g *gitHistory) commitPath(relativePath, message string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	wt, err := g.repo.Worktree()
	if err != nil {
		return fmt.Errorf("git worktree: %w", err)
	}
	if err := wt.AddWithOptions(&gogit.AddOptions{Path: relativePath}); err != nil {
		return fmt.Errorf("git add %s: %w", relativePath, err)
	}
	return g.doCommit(wt, message)
}

// commitAll stages every change in the worktree and commits.
func (g *gitHistory) commitAll(message string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	wt, err := g.repo.Worktree()
	if err != nil {
		return fmt.Errorf("git worktree: %w", err)
	}
	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		return fmt.Errorf("git add -A: %w", err)
	}
	return g.doCommit(wt, message)
}

func (g *gitHistory) doCommit(wt *gogit.Worktree, message string) error {
	sig := object.Signature{Name: "thornotes", Email: "thornotes@localhost", When: time.Now()}
	_, err := wt.Commit(message, &gogit.CommitOptions{
		Author:            &sig,
		Committer:         &sig,
		AllowEmptyCommits: false,
	})
	if errors.Is(err, gogit.ErrEmptyCommit) {
		return nil // nothing staged – that's fine
	}
	if err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// LogFile returns the commit history for the file at relativePath, newest first.
// limit ≤ 0 means no limit.
func (g *gitHistory) LogFile(relativePath string, limit int) ([]model.HistoryEntry, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	iter, err := g.repo.Log(&gogit.LogOptions{
		Order:    gogit.LogOrderCommitterTime,
		FileName: &relativePath,
	})
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	defer iter.Close()

	var entries []model.HistoryEntry
	for {
		commit, err := iter.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("git log iterate: %w", err)
		}
		entries = append(entries, model.HistoryEntry{
			SHA:       commit.Hash.String(),
			Message:   commit.Message,
			Timestamp: commit.Committer.When,
		})
		if limit > 0 && len(entries) >= limit {
			break
		}
	}
	return entries, nil
}

// FileAt returns the content of relativePath at the commit identified by sha.
func (g *gitHistory) FileAt(sha, relativePath string) (string, time.Time, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	commit, err := g.repo.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("get commit %s: %w", sha, err)
	}
	file, err := commit.File(relativePath)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("file at commit: %w", err)
	}
	content, err := file.Contents()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read contents: %w", err)
	}
	return content, commit.Committer.When, nil
}
