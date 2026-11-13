package notes_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqlite_repo "github.com/th0rn0/thornotes/internal/repository/sqlite"
	"github.com/th0rn0/thornotes/internal/wikilinks"
)

func linkRepo(s *serviceStack) *sqlite_repo.LinkRepo {
	return sqlite_repo.NewLinkRepo(s.pool.ReadDB, s.pool.WriteDB)
}

func TestIndexLinks_ResolveAndBacklink(t *testing.T) {
	s := newTestStackFull(t)
	ctx := context.Background()

	b, err := s.svc.CreateNote(ctx, s.userID, "test-uuid", nil, "Target Page", nil)
	require.NoError(t, err)
	a, err := s.svc.CreateNote(ctx, s.userID, "test-uuid", nil, "Source Page", nil)
	require.NoError(t, err)

	_, err = s.svc.UpdateNoteContent(ctx, s.userID, a.ID, "see [[Target Page]]", a.ContentHash)
	require.NoError(t, err)

	out, err := linkRepo(s).ListOutgoing(ctx, s.userID, a.ID)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, b.ID, *out[0].ToNoteID)
	assert.Equal(t, "Target Page", out[0].RawTarget)
	assert.Equal(t, wikilinks.EdgeKey("Target Page"), out[0].TargetHash)
	assert.False(t, out[0].Ambiguous)
	assert.NotNil(t, out[0].ResolvedAt)

	back, err := linkRepo(s).ListBacklinks(ctx, s.userID, b.ID)
	require.NoError(t, err)
	require.Len(t, back, 1)
	assert.Equal(t, a.ID, back[0].FromNoteID)
	assert.Equal(t, "Source Page", back[0].FromTitle)
}

func TestIndexLinks_Unresolved(t *testing.T) {
	s := newTestStackFull(t)
	ctx := context.Background()

	a, err := s.svc.CreateNote(ctx, s.userID, "test-uuid", nil, "Source", nil)
	require.NoError(t, err)
	_, err = s.svc.UpdateNoteContent(ctx, s.userID, a.ID, "see [[Does Not Exist]]", a.ContentHash)
	require.NoError(t, err)

	out, err := linkRepo(s).ListOutgoing(ctx, s.userID, a.ID)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Nil(t, out[0].ToNoteID)
	assert.False(t, out[0].Ambiguous)
}

func TestIndexLinks_RenameMovesInboundLink(t *testing.T) {
	s := newTestStackFull(t)
	ctx := context.Background()

	b, err := s.svc.CreateNote(ctx, s.userID, "test-uuid", nil, "Old Title", nil)
	require.NoError(t, err)
	a, err := s.svc.CreateNote(ctx, s.userID, "test-uuid", nil, "Source", nil)
	require.NoError(t, err)
	_, err = s.svc.UpdateNoteContent(ctx, s.userID, a.ID, "see [[Old Title]]", a.ContentHash)
	require.NoError(t, err)

	out, err := linkRepo(s).ListOutgoing(ctx, s.userID, a.ID)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.NotNil(t, out[0].ToNoteID)

	// Rename the target — the link that named it follows the new title, so the
	// edge still resolves and the backlink survives the rename (ticket 633).
	err = s.svc.UpdateNoteMetadata(ctx, s.userID, "test-uuid", b.ID, "New Title", nil)
	require.NoError(t, err)

	note, err := s.svc.GetNote(ctx, s.userID, a.ID)
	require.NoError(t, err)
	assert.Equal(t, "see [[New Title]]", note.Content)

	out, err = linkRepo(s).ListOutgoing(ctx, s.userID, a.ID)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.NotNil(t, out[0].ToNoteID, "rewriting the link keeps the target resolved")
	assert.Equal(t, b.ID, *out[0].ToNoteID)
	assert.Equal(t, "New Title", out[0].RawTarget)

	back, err := s.svc.Backlinks(ctx, s.userID, b.ID)
	require.NoError(t, err)
	require.Len(t, back, 1)
	assert.Equal(t, a.ID, back[0].FromNoteID)
}

func TestIndexLinks_DeleteNoteCascade(t *testing.T) {
	s := newTestStackFull(t)
	ctx := context.Background()

	b, err := s.svc.CreateNote(ctx, s.userID, "test-uuid", nil, "Target", nil)
	require.NoError(t, err)
	a, err := s.svc.CreateNote(ctx, s.userID, "test-uuid", nil, "Source", nil)
	require.NoError(t, err)
	_, err = s.svc.UpdateNoteContent(ctx, s.userID, a.ID, "see [[Target]]", a.ContentHash)
	require.NoError(t, err)

	// Deleting the target sets inbound edges' to_note_id to NULL (FK SET NULL).
	err = s.svc.DeleteNote(ctx, s.userID, b.ID)
	require.NoError(t, err)

	out, err := linkRepo(s).ListOutgoing(ctx, s.userID, a.ID)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Nil(t, out[0].ToNoteID)

	// Deleting the source cascades its outgoing edges away (FK CASCADE).
	err = s.svc.DeleteNote(ctx, s.userID, a.ID)
	require.NoError(t, err)

	out, err = linkRepo(s).ListOutgoing(ctx, s.userID, a.ID)
	require.NoError(t, err)
	assert.Len(t, out, 0)
}

func TestReconcile_ReindexesLinksAndTitleKey(t *testing.T) {
	s := newTestStackFull(t)
	ctx := context.Background()

	a, err := s.svc.CreateNote(ctx, s.userID, "test-uuid", nil, "Alpha", nil)
	require.NoError(t, err)
	b, err := s.svc.CreateNote(ctx, s.userID, "test-uuid", nil, "Beta", nil)
	require.NoError(t, err)
	_, err = s.svc.UpdateNoteContent(ctx, s.userID, a.ID, "links to [[Beta]]", a.ContentHash)
	require.NoError(t, err)

	// Simulate a freshly-migrated DB: title_key empty, note_links cleared.
	_, err = s.pool.WriteDB.Exec(`UPDATE notes SET title_key = ''`)
	require.NoError(t, err)
	_, err = s.pool.WriteDB.Exec(`DELETE FROM note_links`)
	require.NoError(t, err)

	// Reconcile backfills title keys and rebuilds the graph.
	_, err = s.svc.Reconcile(ctx, s.user)
	require.NoError(t, err)

	var empty int
	err = s.pool.ReadDB.QueryRow(`SELECT COUNT(*) FROM notes WHERE title_key = ''`).Scan(&empty)
	require.NoError(t, err)
	assert.Zero(t, empty, "reconcile should backfill title_key for every note")

	out, err := linkRepo(s).ListOutgoing(ctx, s.userID, a.ID)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, b.ID, *out[0].ToNoteID)
}

func TestBacklinks_QueryableByEdgeKeyAndTitleKey(t *testing.T) {
	s := newTestStackFull(t)
	ctx := context.Background()

	b, err := s.svc.CreateNote(ctx, s.userID, "test-uuid", nil, "Target Page", nil)
	require.NoError(t, err)
	a, err := s.svc.CreateNote(ctx, s.userID, "test-uuid", nil, "Source", nil)
	require.NoError(t, err)
	_, err = s.svc.UpdateNoteContent(ctx, s.userID, a.ID, "see [[Target Page]]", a.ContentHash)
	require.NoError(t, err)

	// By edge_key: target_hash = sha256(normalize("Target Page")).
	rows, err := linkRepo(s).ListByTargetHash(ctx, s.userID, wikilinks.EdgeKey("Target Page"))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, a.ID, rows[0].FromNoteID)

	// By title_key: the normalized title resolves the note, then its backlinks.
	var targetID int64
	err = s.pool.ReadDB.QueryRow(
		`SELECT id FROM notes WHERE user_id = ? AND title_key = ?`,
		s.userID, "target page",
	).Scan(&targetID)
	require.NoError(t, err)
	assert.Equal(t, b.ID, targetID)

	back, err := linkRepo(s).ListBacklinks(ctx, s.userID, targetID)
	require.NoError(t, err)
	require.Len(t, back, 1)
	assert.Equal(t, a.ID, back[0].FromNoteID)
}

func TestCreateNote_SetsTitleKey(t *testing.T) {
	s := newTestStackFull(t)
	ctx := context.Background()

	n, err := s.svc.CreateNote(ctx, s.userID, "test-uuid", nil, "Hello — World", nil)
	require.NoError(t, err)
	assert.Equal(t, "hello - world", n.TitleKey)

	var tk string
	err = s.pool.ReadDB.QueryRow(`SELECT title_key FROM notes WHERE id = ?`, n.ID).Scan(&tk)
	require.NoError(t, err)
	assert.Equal(t, "hello - world", tk)
}
