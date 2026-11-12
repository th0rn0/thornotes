package notes

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
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
	root string     // worktree root; relative paths below are resolved against it
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
		if err := commitInitialIgnore(repo); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("git open: %w", err)
	}
	return &gitHistory{repo: repo, root: dir}, nil
}

// commitInitialIgnore records the .gitignore thornotes writes at init. It is
// infrastructure the store creates itself, so a fresh repository starts with a
// clean working tree instead of one permanently carrying an untracked file that
// the next delete or rename would otherwise sweep into its commit.
func commitInitialIgnore(repo *gogit.Repository) error {
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("git worktree: %w", err)
	}
	if _, err := wt.Add(".gitignore"); err != nil {
		return fmt.Errorf("git add .gitignore: %w", err)
	}
	sig := object.Signature{Name: "thornotes", Email: "thornotes@localhost", When: time.Now()}
	_, err = wt.Commit("init: thornotes history", &gogit.CommitOptions{
		Author:            &sig,
		Committer:         &sig,
		AllowEmptyCommits: false,
	})
	if errors.Is(err, gogit.ErrEmptyCommit) {
		return nil // an existing .gitignore was already in the index
	}
	if err != nil {
		return fmt.Errorf("git commit init: %w", err)
	}
	return nil
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
	_, err = f.WriteString(".thornotes-*.tmp\n.thornotes-probe-*\n.thornotes-trash/\n")
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
	return g.commitPaths("save: "+relativePath, relativePath)
}

// CommitDelete stages the removal of relativePath and records a "delete:"
// commit. relativePath may be a note's file or a folder: a removed folder is
// recorded as the removal of every file it still had in the index.
func (g *gitHistory) CommitDelete(relativePath string) error {
	return g.commitPaths("delete: "+relativePath, relativePath)
}

// CommitRename stages the moved paths (both ends of the rename) and records a
// "rename:" commit.
func (g *gitHistory) CommitRename(oldPath, newPath string) error {
	return g.commitPaths("rename: "+oldPath+" -> "+newPath, oldPath, newPath)
}

// commitPaths stages exactly the paths the operation changed and commits them.
//
// It deliberately does not stage the rest of the working tree: a note the user
// is editing outside the API is their unsaved work, and letting it ride along
// makes the delete/rename commit describe a change it did not make. Nothing here
// is also a destructive git command — the index is only ever added to or have
// specific entries removed, so a failure cannot lose another file's edits.
func (g *gitHistory) commitPaths(message string, paths ...string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	wt, err := g.repo.Worktree()
	if err != nil {
		return fmt.Errorf("git worktree: %w", err)
	}
	for _, p := range paths {
		if err := g.stagePath(wt, p); err != nil {
			return err
		}
	}
	return g.doCommit(wt, message)
}

// stagePath stages one path: a path still present in the live tree is added
// (recursively, for a directory). A path gone from the live tree is recorded as
// the removal of the files it still has in the index, because git has no index
// entry for a directory and Add is a silent no-op for a missing untracked path.
func (g *gitHistory) stagePath(wt *gogit.Worktree, relativePath string) error {
	rel := path.Clean(relativePath)
	// Never let an empty or escaping path widen into the whole tree: path.Clean
	// turns "" into ".", and adding "." stages every file in the vault.
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(g.root, filepath.FromSlash(rel))); err == nil {
		if err := wt.AddWithOptions(&gogit.AddOptions{Path: rel}); err != nil {
			return fmt.Errorf("git add %s: %w", rel, err)
		}
		return nil
	}
	removed, err := g.trackedUnder(rel)
	if err != nil {
		return err
	}
	for _, p := range removed {
		if err := wt.AddWithOptions(&gogit.AddOptions{Path: p}); err != nil {
			return fmt.Errorf("git add %s: %w", p, err)
		}
	}
	return nil
}

// trackedUnder returns the index entries at relativePath or beneath it, so a
// deleted or renamed subtree can be staged file by file.
func (g *gitHistory) trackedUnder(relativePath string) ([]string, error) {
	idx, err := g.repo.Storer.Index()
	if err != nil {
		return nil, fmt.Errorf("git index: %w", err)
	}
	prefix := relativePath + "/"
	var out []string
	for _, e := range idx.Entries {
		if e.Name == relativePath || strings.HasPrefix(e.Name, prefix) {
			out = append(out, e.Name)
		}
	}
	sort.Strings(out)
	return out, nil
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
