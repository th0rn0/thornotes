package notes_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/hub"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository/sqlite"
)

func TestWatch_DetectsFileChange(t *testing.T) {
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	notesDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(notesDir)
	require.NoError(t, err)

	userRepo := sqlite.NewUserRepo(pool.WriteDB)
	folderRepo := sqlite.NewFolderRepo(pool.ReadDB, pool.WriteDB)
	noteRepo := sqlite.NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := sqlite.NewSearchRepo(pool.ReadDB, pool.WriteDB)

	svc := notes.NewService(noteRepo, folderRepo, searchRepo, fs)

	// Create a user and a note.
	user, err := userRepo.Create(context.Background(), "alice", "hash")
	require.NoError(t, err)

	note, err := svc.CreateNote(context.Background(), user.ID, nil, "Watch Test", nil)
	require.NoError(t, err)

	h := hub.New()
	ch, unsub := h.Subscribe(user.ID)
	defer unsub()

	// Modify the file on disk directly (simulates external editor).
	err = fs.Write(note.DiskPath, "# Changed by external editor\n\nNew content.")
	require.NoError(t, err)

	// Run one watcher tick with a very short interval.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go notes.Watch(ctx, 50*time.Millisecond, svc, userRepo, h)

	select {
	case event := <-ch:
		assert.Equal(t, "notes_changed", event)
	case <-ctx.Done():
		t.Fatal("watcher did not notify within timeout")
	}

	// Verify DB was updated.
	updated, err := noteRepo.GetByID(context.Background(), user.ID, note.ID)
	require.NoError(t, err)
	assert.Contains(t, updated.Content, "Changed by external editor")
}

func TestWatch_NoNotifyWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	notesDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(notesDir)
	require.NoError(t, err)

	userRepo := sqlite.NewUserRepo(pool.WriteDB)
	folderRepo := sqlite.NewFolderRepo(pool.ReadDB, pool.WriteDB)
	noteRepo := sqlite.NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := sqlite.NewSearchRepo(pool.ReadDB, pool.WriteDB)

	svc := notes.NewService(noteRepo, folderRepo, searchRepo, fs)

	user, err := userRepo.Create(context.Background(), "bob", "hash")
	require.NoError(t, err)

	_, err = svc.CreateNote(context.Background(), user.ID, nil, "Unchanged Note", nil)
	require.NoError(t, err)

	h := hub.New()
	ch, unsub := h.Subscribe(user.ID)
	defer unsub()

	// Don't change anything on disk — watcher should stay silent.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go notes.Watch(ctx, 30*time.Millisecond, svc, userRepo, h)

	select {
	case event := <-ch:
		t.Fatalf("unexpected notification %q — file was not changed", event)
	case <-ctx.Done():
		// Correct: no notification received.
	}
}

func TestWatch_StopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	notesDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(notesDir)
	require.NoError(t, err)

	userRepo := sqlite.NewUserRepo(pool.WriteDB)
	folderRepo := sqlite.NewFolderRepo(pool.ReadDB, pool.WriteDB)
	noteRepo := sqlite.NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := sqlite.NewSearchRepo(pool.ReadDB, pool.WriteDB)

	svc := notes.NewService(noteRepo, folderRepo, searchRepo, fs)
	h := hub.New()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		notes.Watch(ctx, 10*time.Millisecond, svc, userRepo, h)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Correct: Watch returned after context cancel.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Watch did not stop after context cancel")
	}
}
