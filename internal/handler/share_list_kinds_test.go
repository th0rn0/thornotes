package handler_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/model"
)

// GET /api/v1/shares is ONE list of every link the caller has handed out
// (ticket 616, slice 3).
//
// Before this, the endpoint answered note shares only, so a folder link was
// unlistable and unrevocable unless its owner remembered which folder had been
// shared — and a folder link is recursive, so forgetting one left a whole
// subtree exposed with no way to find it. The security review of slice 1 filed
// that as the first unlistable capability in the product.
//
// What these tests pin, and why each one is here:
//
//   - both kinds come back, each tagged with `kind`, each carrying the
//     identifier a revoke needs, and the folder's name as its title;
//   - the note half is byte-compatible with what the panel already reads
//     (note_id, token_prefix, expires_at, last_viewed_at) — the fields were
//     added to, never renamed;
//   - a revoked link of either kind is gone (revoking is a DELETE, so there is
//     nothing left to list);
//   - an expired link of either kind is answered the SAME way, and that way is
//     the note half's existing rule: it stays in the list with its deadline
//     visible. The ticket asked for expired links to be absent "matching what
//     the note half already does", but the note half does not do that — it lists
//     them, deliberately, and asserts it in
//     TestShareView_ExpiredLinkStillResolvesForItsOwner ("the dead deadline must
//     still be visible, or it cannot be revoked"). Filtering one half and not
//     the other would make this endpoint answer two different questions
//     depending on `kind`, so both halves list and both are tested here;
//   - the order is total, so two identical reads answer the same sequence;
//   - ownership: another account's links of either kind never appear.

// shareByKind indexes a share list by kind, failing the test if either kind is
// missing or appears twice.
func shareByKind(t *testing.T, shares []model.ShareLink, kind string) model.ShareLink {
	t.Helper()
	var found []model.ShareLink
	for _, s := range shares {
		if s.Kind == kind {
			found = append(found, s)
		}
	}
	require.Len(t, found, 1, "expected exactly one %q row in %+v", kind, shares)
	return found[0]
}

// TestShareList_BothKinds_TaggedAndIdentified is the shape assertion for the
// whole slice: one row per link, kind says which surface it resolves on, and
// each row carries the identifier that surface's revoke route takes.
func TestShareList_BothKinds_TaggedAndIdentified(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	noteToken := shareNote(t, f.client, f.noteID, nil)
	folderToken := shareFolder(t, f.client, f.folderID, nil)

	shares := listShares(t, f.client)
	require.Len(t, shares, 2, "one note link and one folder link, and nothing else")

	note := shareByKind(t, shares, model.ShareKindNote)
	require.NotNil(t, note.NoteID, "a note row carries the note's id")
	assert.Equal(t, f.noteID, *note.NoteID)
	assert.Equal(t, "Alice private note", note.Title)
	assert.Equal(t, noteToken[:8]+"…", note.TokenPrefix, "the note half is unchanged")
	assert.Nil(t, note.ExpiresAt, "no deadline is still null, not a zero time")
	assert.Nil(t, note.LastViewedAt)
	assert.False(t, note.UpdatedAt.IsZero())

	folder := shareByKind(t, shares, model.ShareKindFolder)
	assert.Nil(t, folder.NoteID, "a folder link is not a note link: its note_id is null")
	require.NotNil(t, folder.FolderID, "a folder row carries the folder the revoke route needs")
	assert.Equal(t, f.folderID, *folder.FolderID)
	assert.Equal(t, "Alice folder", folder.Title, "the folder's name is the title to display")
	assert.Equal(t, folderToken[:8]+"…", folder.TokenPrefix, "the token is redacted here too")
	assert.Nil(t, folder.ExpiresAt)
	assert.Nil(t, folder.LastViewedAt)
	assert.False(t, folder.UpdatedAt.IsZero(), "a folder row's updated_at is its share row's created_at")

	// One authenticated read must not be a bulk export of the capabilities
	// themselves, whichever kind it is.
	for _, s := range shares {
		assert.NotEqual(t, noteToken, s.TokenPrefix)
		assert.NotEqual(t, folderToken, s.TokenPrefix)
		assert.NotEqual(t, folderToken[:8], folderToken, "sanity: the prefix is a prefix")
	}
}

// TestShareList_RevokedLinksAreGone checks that a revoked link of either kind
// leaves the list. Revoking is a DELETE on both surfaces, so this is the
// property "what you are shown is what currently exists", not a filter.
func TestShareList_RevokedLinksAreGone(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	shareNote(t, f.client, f.noteID, nil)
	shareFolder(t, f.client, f.folderID, nil)
	require.Len(t, listShares(t, f.client), 2)

	revokeFolderShare(t, f.client, f.folderID)
	remaining := listShares(t, f.client)
	require.Len(t, remaining, 1, "the revoked folder link must be gone")
	assert.Equal(t, model.ShareKindNote, remaining[0].Kind)

	// And the note half answers the same way, through its own route.
	resp := f.client.do(t, http.MethodPost, "/api/v1/notes/"+i64str(f.noteID)+"/share",
		map[string]any{"clear": true})
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Empty(t, listShares(t, f.client), "both revoked links must be absent")
}

// TestShareList_ExpiredLink_AnsweredByBothKindsAlike pins the one rule this
// endpoint has about deadlines: a dead link stays visible, with the deadline
// that explains why it stopped answering.
//
// The note half has done this since it existed (see
// TestShareView_ExpiredLinkStillResolvesForItsOwner, whose comment says the
// deadline "must still be visible, or it cannot be revoked"). The folder half
// matches it here rather than inventing a second rule for the same list, so a
// reader of either kind gets the same answer to "what have I handed out".
func TestShareList_ExpiredLink_AnsweredByBothKindsAlike(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	noteToken := shareNote(t, f.client, f.noteID, map[string]any{
		"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})
	folderToken := shareFolder(t, f.client, f.folderID, nil)

	expireShare(t, f.client.pool, f.noteID)
	expireFolderShare(t, f.client.pool, f.folderID)

	// Both links are dead to the world...
	for _, path := range []string{"/s/" + noteToken, "/f/" + folderToken} {
		resp := anonGet(t, f.client, path)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "%s must stop resolving", path)
		resp.Body.Close()
	}

	// ...and both are still listed, with their deadlines, exactly as the note
	// half has always listed one.
	shares := listShares(t, f.client)
	require.Len(t, shares, 2)

	note := shareByKind(t, shares, model.ShareKindNote)
	require.NotNil(t, note.ExpiresAt, "the dead deadline must still be visible")

	folder := shareByKind(t, shares, model.ShareKindFolder)
	require.NotNil(t, folder.ExpiresAt, "the folder half answers the same way as the note half")
}

// TestShareList_OrderIsStableAcrossReads pins the documented order: it is a
// total order, so the same set of links always comes back in the same sequence.
// A list that reshuffles between two refreshes is a UI bug waiting to happen.
//
// Ties are forced rather than hoped for: a folder share and a note share stamped
// in the same second are the real case (SQLite's DATETIME has second
// resolution), and the rule after the timestamp is kind, then id.
func TestShareList_OrderIsStableAcrossReads(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	// Two notes and two folders, so both halves have more than one row and the
	// id tie-breaker is exercised.
	secondNote := isolationCreateNote(t, f.client, &f.folderID, "Second shared note")
	secondFolder := isolationCreateFolder(t, f.client, "Alice second folder")
	shareNote(t, f.client, f.noteID, nil)
	shareNote(t, f.client, secondNote, nil)
	shareFolder(t, f.client, f.folderID, nil)
	shareFolder(t, f.client, secondFolder, nil)

	first := listShares(t, f.client)
	require.Len(t, first, 4)

	// Every link stamped at the same instant: nothing but the tie-break can
	// decide the order now, and it must decide it the same way twice.
	stamp := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	_, err := f.client.pool.WriteDB.Exec(`UPDATE notes SET updated_at = ? WHERE id IN (?, ?)`,
		stamp, f.noteID, secondNote)
	require.NoError(t, err)
	_, err = f.client.pool.WriteDB.Exec(`UPDATE folder_shares SET created_at = ?`, stamp)
	require.NoError(t, err)

	first = listShares(t, f.client)
	second := listShares(t, f.client)
	require.Len(t, first, 4)
	assert.Equal(t, first, second, "two identical reads must answer the same sequence")

	for i := 1; i < len(first); i++ {
		prev, cur := first[i-1], first[i]
		if cur.UpdatedAt.After(prev.UpdatedAt) {
			t.Fatalf("row %d is newer than row %d: order is updated_at DESC", i, i-1)
		}
		if cur.UpdatedAt.Equal(prev.UpdatedAt) && cur.Kind != prev.Kind {
			assert.Less(t, prev.Kind, cur.Kind, "ties break on kind, ascending")
		}
	}

	// The documented tie-break, stated once explicitly so a change to it is a
	// test failure rather than a surprise: "folder" sorts before "note".
	var kinds []string
	for _, s := range first {
		if s.UpdatedAt.Equal(stamp) {
			kinds = append(kinds, s.Kind)
		}
	}
	require.Len(t, kinds, 4, "every row carries the forced stamp")
	assert.Equal(t, []string{
		model.ShareKindFolder, model.ShareKindFolder,
		model.ShareKindNote, model.ShareKindNote,
	}, kinds)
}

// TestShareList_OtherUsersLinksNeverAppear is the ownership assertion, for both
// kinds: the list is scoped by user_id in SQL on each half, so it cannot answer
// with a link somebody else handed out.
func TestShareList_OtherUsersLinksNeverAppear(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	// Alice shares one of each.
	shareNote(t, f.client, f.noteID, nil)
	shareFolder(t, f.client, f.folderID, nil)
	require.Len(t, listShares(t, f.client), 2)

	// Bob shares one of each of his own, and sees only his.
	f.client.login(t, "bob", isolationBobPassword)
	bobNote := isolationCreateNote(t, f.client, nil, "Bob shared note")
	bobFolder := isolationCreateFolder(t, f.client, "Bob folder")
	shareNote(t, f.client, bobNote, nil)
	shareFolder(t, f.client, bobFolder, nil)

	bobShares := listShares(t, f.client)
	require.Len(t, bobShares, 2)
	for _, s := range bobShares {
		switch s.Kind {
		case model.ShareKindNote:
			require.NotNil(t, s.NoteID)
			assert.NotEqual(t, f.noteID, *s.NoteID, "alice's note must not appear in bob's list")
		case model.ShareKindFolder:
			require.NotNil(t, s.FolderID)
			assert.NotEqual(t, f.folderID, *s.FolderID, "alice's folder must not appear in bob's list")
		default:
			t.Fatalf("unexpected kind %q", s.Kind)
		}
	}
	assert.Equal(t, "Bob shared note", shareByKind(t, bobShares, model.ShareKindNote).Title)
	assert.Equal(t, "Bob folder", shareByKind(t, bobShares, model.ShareKindFolder).Title)

	// And alice's list is unchanged by bob's two shares.
	f.asAlice(t)
	aliceShares := listShares(t, f.client)
	require.Len(t, aliceShares, 2)
	assert.Equal(t, "Alice private note", shareByKind(t, aliceShares, model.ShareKindNote).Title)
	assert.Equal(t, "Alice folder", shareByKind(t, aliceShares, model.ShareKindFolder).Title)
}
