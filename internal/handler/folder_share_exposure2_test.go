package handler_test

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/model"
)

// Folder shares, slice 4 (ticket 616): the exposure pack, part 2.
//
// The three assertions in this file are the ones the finished surface still
// owed a test for:
//
//  1. a nested subfolder three levels down is reachable by walking, and the
//     keyset cursor cannot be used to walk out of the shared root;
//  2. the list and the read path agree about a dead link — the owner is told
//     the link is dead, and the link answers dead, in the same breath;
//  3. last_viewed_at is bounded under load: N concurrent reads of one link
//     stamp it at most once per window, and a stamp is never in the future.
//
// The file shares its fixtures with the suites next door (newFolderShareFixture,
// expireFolderShare, expireShare, shareByKind, storedFolderView, createSubfolder)
// rather than building a harness of its own: a second fixture tree would be a
// second thing that can be wrong.

// neverIssuedToken is a token that was never issued. It is the reference
// answer every refusal on the anonymous surface is compared against, because
// an equal status code is not the assertion — the whole response is.
const neverIssuedToken = "0123456789abcdef0123456789abcdef"

// ─── 1. the walk, and the cursor that must not leave the tree ───────────────

// TestFolderShare_NestedSubfolderThreeLevelsDown_ReachableByWalking is the
// recursion promise stated at its deepest: a subfolder three levels below the
// shared root is reachable by walking down, one level at a time.
//
// The fixture's tree is two levels deep (work/ > projects/), so the test adds
// two more with the same helper the other suite uses, and the deepest note is
// then three levels below the shared root. The walk is asserted level by
// level, because "reachable" and "inlined" are different claims: a level that
// inlined its descendants would answer the same request but would publish the
// whole subtree in one page, which is the shape the contract refused.
//
// The second half is the escape attempt. The keyset cursor is a NAME (the last
// folder name or note slug on the page), not a path, so the only way it could
// leave the tree is by being used as one — and the answer to every such
// attempt must be the uniform 404, byte for byte the answer an unknown token
// gets, rather than a listing of something outside the shared root.
func TestFolderShare_NestedSubfolderThreeLevelsDown_ReachableByWalking(t *testing.T) {
	f := newFolderShareFixture(t)
	f.client.login(t, "alice", isolationAlicePassword)

	// Two levels below the shared root, so projects/alpha/beta is three.
	alpha := createSubfolder(t, f.client, "alpha", f.middleID)
	beta := createSubfolder(t, f.client, "beta", alpha)
	isolationCreateNote(t, f.client, &beta, "Deep Note")

	// A note outside the shared tree and a folder beside it, so a cursor that
	// reached either would be visible in the answer.
	outsideNote := isolationCreateNote(t, f.client, nil, "Outside Note")
	require.NotZero(t, outsideNote)
	outsideFolder := isolationCreateFolder(t, f.client, "elsewhere")
	isolationCreateNote(t, f.client, &outsideFolder, "Elsewhere Note")

	t.Run("the walk reaches three levels down", func(t *testing.T) {
		// Level 0 — the shared root: one link down, nothing inlined.
		root := readAnonymousPage(t, f.client, "/f/"+f.token)
		require.Contains(t, root, `href="/f/`+f.token+`/d/projects"`,
			"the shared root must offer its subfolder as a link down")
		assert.NotContains(t, root, "alpha",
			"a subfolder below the one being listed must not be inlined into it")
		assert.NotContains(t, root, "Deep Note",
			"a note three levels down must not be inlined into the root listing")

		// Level 1.
		one := readAnonymousPage(t, f.client, "/f/"+f.token+"/d/projects")
		require.Contains(t, one, `href="/f/`+f.token+`/d/projects/alpha"`,
			"level 1 must offer its own subfolder as a link down")
		assert.NotContains(t, one, "Deep Note",
			"a note two levels below the level being listed must not be inlined")

		// Level 2.
		two := readAnonymousPage(t, f.client, "/f/"+f.token+"/d/projects/alpha")
		require.Contains(t, two, `href="/f/`+f.token+`/d/projects/alpha/beta"`,
			"level 2 must offer its own subfolder as a link down")
		assert.NotContains(t, two, "Deep Note",
			"a note one level below the level being listed must not be inlined")

		// Level 3 — three levels below the shared root.
		three := readAnonymousPage(t, f.client, "/f/"+f.token+"/d/projects/alpha/beta")
		assert.Contains(t, three, "Deep Note",
			"the note at the bottom of the walk must be listed at its own level")
		assert.Contains(t, three, `href="/f/`+f.token+`/n/projects/alpha/beta/deep-note"`,
			"the deep note must be addressable under the share token")

		// And the note itself reads.
		note := readAnonymousPage(t, f.client, "/f/"+f.token+"/n/projects/alpha/beta/deep-note")
		assert.Contains(t, note, "Deep Note",
			"the note at the bottom of the walk must be readable through the link")

		// No level of the walk names an ancestor of the shared root.
		for name, page := range map[string]string{
			"root":   root,
			"level1": one,
			"level2": two,
			"level3": three,
		} {
			assert.NotContains(t, page, "topsecret",
				"%s must not name a folder above the shared root", name)
		}
	})

	t.Run("a crafted cursor cannot walk out of the shared root", func(t *testing.T) {
		// The cursor is a name, so these are the shapes a walker could send.
		// The traversal shapes matter even though the cursor is not a path:
		// the assertion is that NOTHING outside the tree is ever listed, and
		// that the answer to an escape attempt is the uniform 404.
		cursors := []string{
			"na=" + urlEscape("../../../../etc/passwd"),
			"na=" + urlEscape("%2e%2e%2f%2e%2e%2ftopsecret"),
			"na=" + urlEscape("../projects"),
			"na=outside-note",
			"na=elsewhere-note",
			"na=" + urlEscape("a\x00b"),
			"na=" + strings.Repeat("z", 4096),
			"na=zzzzz",
			"fa=" + urlEscape("../../topsecret"),
			"fa=elsewhere",
		}

		for _, cursor := range cursors {
			cursor := cursor
			t.Run(cursor, func(t *testing.T) {
				resp := anonGet(t, f.client, "/f/"+f.token+"?"+cursor)
				body := captureBody(t, resp)

				// Whatever the cursor is, the answer is one of the two the
				// surface is allowed to give: the listing, or the uniform
				// refusal. A third answer would be a new oracle.
				require.Contains(t,
					[]int{http.StatusOK, http.StatusNotFound},
					resp.StatusCode,
					"a crafted cursor must answer the listing or the uniform 404")

				if resp.StatusCode == http.StatusNotFound {
					// The refusal is the uniform one: whole serialised
					// response, so no header and no byte differs from a
					// token that never existed.
					reference := serialiseResponse(t, anonGet(t, f.client, "/f/"+neverIssuedToken))
					got := serialiseResponse(t, anonGet(t, f.client, "/f/"+f.token+"?"+cursor))
					assert.Equal(t, reference, got,
						"a crafted cursor must be refused exactly what an unknown token is refused")
					return
				}

				// A listing: it must be the shared tree's own, and nothing else.
				assert.NotContains(t, string(body), "topsecret",
					"a crafted cursor must not surface a folder above the shared root")
				assert.NotContains(t, string(body), "Outside Note",
					"a crafted cursor must not surface a note outside the shared tree")
				assert.NotContains(t, string(body), "Elsewhere Note",
					"a crafted cursor must not surface a note in a sibling folder")
				assert.NotContains(t, string(body), "elsewhere",
					"a crafted cursor must not surface a folder outside the shared tree")
				assertEveryLinkUnderToken(t, string(body), f.token)
			})
		}
	})

	t.Run("walking past the end answers the uniform 404", func(t *testing.T) {
		// The cursor travelling with a walk past the end of the tree is the
		// escape attempt the uniform refusal exists for: past the end there
		// is nothing to list, and the answer must be the same 404 an unknown
		// token gets rather than a listing of something outside the tree.
		reference := serialiseResponse(t, anonGet(t, f.client, "/f/"+neverIssuedToken))
		pastTheEnd := []string{
			"/f/" + f.token + "/d/projects/../../topsecret",
			"/f/" + f.token + "/d/projects/alpha/beta/gamma",
			"/f/" + f.token + "/n/projects/alpha/beta/../../../etc/passwd",
			"/f/" + f.token + "/d/..",
		}
		for _, path := range pastTheEnd {
			path := path
			t.Run(path, func(t *testing.T) {
				for _, cursor := range []string{"", "?na=" + urlEscape("../../../../etc/passwd")} {
					got := serialiseResponse(t, anonGet(t, f.client, path+cursor))
					assert.Equal(t, reference, got,
						"walking past the end of the tree must answer the uniform 404, "+
							"with or without a crafted cursor: %s", path+cursor)
				}
			})
		}
	})
}

// urlEscape escapes a cursor value for a query string, keeping the shapes
// under test intact: a traversal shape stays a traversal shape, and a NUL
// stays a NUL.
func urlEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteString("%")
			const hex = "0123456789ABCDEF"
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}

// assertEveryLinkUnderToken checks that every link the anonymous page emits
// stays inside this share token — the exposure rule the whole surface is built
// on, asserted on the rendered bytes rather than on the code that built them.
func assertEveryLinkUnderToken(t *testing.T, body, token string) {
	t.Helper()
	for _, href := range hrefsIn(body) {
		if strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "/static/") {
			continue
		}
		assert.True(t, strings.HasPrefix(href, "/f/"+token),
			"every link must stay under this share token, got %q", href)
	}
}

// hrefsIn returns every href in a page.
func hrefsIn(body string) []string {
	var out []string
	rest := body
	for {
		i := strings.Index(rest, `href="`)
		if i < 0 {
			return out
		}
		rest = rest[i+len(`href="`):]
		j := strings.Index(rest, `"`)
		if j < 0 {
			return out
		}
		out = append(out, rest[:j])
		rest = rest[j+1:]
	}
}

// ─── 2. the list and the read path agree about a dead link ──────────────────

// TestFolderShareExpiry_ListAndReadPath_AgreeOnADeadLink is the "list and
// the read path cannot disagree" assertion.
//
// The rule it pins: a link that has expired is DEAD everywhere at once. The
// owner's list must not describe a dead link as live — that is the half that
// would have somebody paste a URL that no longer works — and the anonymous
// surface must stop answering it in the same breath, with the uniform 404.
//
// The list answers the owner's question ("what have I handed out?"), so a
// dead link stays in it, carrying the deadline that explains why it stopped
// answering — the same rule the note half has always had, and the reason
// TestShareList_ExpiredLink_AnsweredByBothKindsAlike exists. What the list
// must never do is present a dead link as a live one: an expired row carries a
// deadline in the past, and the read path answers 404 for exactly that link.
// That is the agreement being asserted: the deadline the list shows predicts
// the answer the link gives.
//
// Both kinds are asserted, because the two halves of GET /api/v1/shares are
// one list and a change to one half alone would make the endpoint answer two
// different questions.
func TestFolderShareExpiry_ListAndReadPath_AgreeOnADeadLink(t *testing.T) {
	t.Run("a live link is listed as live and resolves", func(t *testing.T) {
		f := newFolderShareFixture(t)
		f.client.login(t, "alice", isolationAlicePassword)

		// A note link beside the folder link, so both kinds of row are in
		// the one list.
		noteID := isolationCreateNote(t, f.client, &f.rootID, "Owner note")
		noteToken := shareNote(t, f.client, noteID, nil)

		shares := listShares(t, f.client)
		require.Len(t, shares, 2)

		note := shareByKind(t, shares, model.ShareKindNote)
		assert.Nil(t, note.ExpiresAt, "a live note link must carry no deadline")
		assert.Nil(t, note.LastViewedAt)

		folder := shareByKind(t, shares, model.ShareKindFolder)
		assert.Nil(t, folder.ExpiresAt, "a live folder link must carry no deadline")
		assert.Nil(t, folder.LastViewedAt)

		status, _ := anonBody(t, f.client, "/s/"+noteToken)
		assert.Equal(t, http.StatusOK, status, "a live note link must resolve")
		status, _ = anonBody(t, f.client, "/f/"+f.token)
		assert.Equal(t, http.StatusOK, status, "a live folder link must resolve")
	})

	t.Run("an expired link is listed as dead and answers dead", func(t *testing.T) {
		f := newFolderShareFixture(t)
		f.client.login(t, "alice", isolationAlicePassword)

		noteID := isolationCreateNote(t, f.client, &f.rootID, "Owner note")
		noteToken := shareNote(t, f.client, noteID, map[string]any{
			"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		})

		// Both links are live to the world before the clock moves.
		status, _ := anonBody(t, f.client, "/s/"+noteToken)
		require.Equal(t, http.StatusOK, status)
		status, _ = anonBody(t, f.client, "/f/"+f.token)
		require.Equal(t, http.StatusOK, status)

		expireShare(t, f.client.pool, noteID)
		expireFolderShare(t, f.client.pool, f.rootID)

		// The list: both rows are still there — the owner has to be able to
		// find a link they handed out in order to revoke it — and both carry
		// a deadline in the PAST. A dead link described with no deadline, or
		// with one in the future, would be the list and the read path
		// disagreeing.
		shares := listShares(t, f.client)
		require.Len(t, shares, 2)

		note := shareByKind(t, shares, model.ShareKindNote)
		require.NotNil(t, note.ExpiresAt, "the dead deadline must still be visible")
		assert.True(t, note.ExpiresAt.Before(time.Now().UTC()),
			"the listed deadline must be in the past: %s", note.ExpiresAt)

		folder := shareByKind(t, shares, model.ShareKindFolder)
		require.NotNil(t, folder.ExpiresAt, "the dead deadline must still be visible")
		assert.True(t, folder.ExpiresAt.Before(time.Now().UTC()),
			"the listed deadline must be in the past: %s", folder.ExpiresAt)

		// The read path, in the same breath: both links answer the uniform
		// 404 — the same answer a token that never existed gets, compared
		// whole, so no header and no byte differs.
		reference := serialiseResponse(t, anonGet(t, f.client, "/f/"+neverIssuedToken))
		for _, path := range []string{"/s/" + noteToken, "/f/" + f.token} {
			got := serialiseResponse(t, anonGet(t, f.client, path))
			assert.Equal(t, reference, got,
				"an expired link must answer exactly what an unknown token answers: %s", path)
		}

		// And the folder's rows stop with it: a dead link is dead for the
		// listing AND for the note underneath it, so a reader who kept the
		// deep URL cannot read past the death of the link.
		for _, path := range []string{
			"/f/" + f.token + "/d/projects",
			"/f/" + f.token + "/n/projects/plan-note",
		} {
			got := serialiseResponse(t, anonGet(t, f.client, path))
			assert.Equal(t, reference, got,
				"a dead folder link must stop resolving its own rows too: %s", path)
		}
	})
}

// ─── 3. last_viewed_at under load ───────────────────────────────────────────

// TestFolderShareView_ConcurrentReads_OneStampPerWindow is the
// write-amplification bound on the anonymous surface.
//
// /f/<token> is unauthenticated and read-heavy: anybody holding a URL can
// request it as fast as they like. If every read wrote, one reader would turn
// the link's row into a write-per-request, and a folder link would hand its
// holder a cheap way to grow the table. The bound is one stamp per link per
// ten minutes — notes.ShareViewWindow — and it has to hold under concurrency,
// which is the case that is easy to get wrong: N requests all read the link's
// current stamp before any of them writes, so all N believe a write is due.
//
// The assertion is on what was WRITTEN, not on what was logged: a counter on
// the row's own column counts the stamps, and the stored value is checked
// twice — that it did not move inside the window, and that it is not in the
// future.
//
// The counter is a trigger on the test's own temporary database, counting the
// writes of this one column for this one row. It is a measurement instrument,
// not a change to the product: the trigger lives in the test's database and
// touches no production code.
func TestFolderShareView_ConcurrentReads_OneStampPerWindow(t *testing.T) {
	f := newFolderShareFixture(t)
	f.client.login(t, "alice", isolationAlicePassword)

	stamps := newFolderShareStampCounter(t, f)

	// The load: N concurrent reads of one link that has never been read, so
	// every one of them believes a stamp is due.
	const readers = 16
	burstStart := time.Now().UTC()
	fireConcurrentFolderReads(t, f, readers)

	// Let every in-flight stamp land before the count is read.
	require.Eventually(t, func() bool {
		n, err := stamps()
		return err == nil && n >= 1
	}, 5*time.Second, 20*time.Millisecond,
		"a view of a live folder link must be recorded")
	time.Sleep(500 * time.Millisecond)

	written, err := stamps()
	require.NoError(t, err)
	assert.Equal(t, 1, written,
		"%d concurrent reads of one link must produce at most one stamp", readers)

	stored := storedFolderView(t, f.client.pool, f.rootID)
	require.NotNil(t, stored, "the link must carry its view stamp")

	// A stamp is never in the future. The server stamps with its own clock,
	// so a stamp ahead of now would mean a clock read the wrong way round —
	// or a value from another machine entirely.
	assert.False(t, stored.After(time.Now().UTC().Add(2*time.Second)),
		"a view stamp must never be in the future: %s", stored)
	assert.False(t, stored.Before(burstStart.Add(-2*time.Second)),
		"the stamp must belong to this burst, not to an earlier read: %s", stored)

	// A second burst, still inside the ten-minute window, must not stamp
	// again: the value the owner sees ("last opened") is a windowed fact,
	// not a counter.
	fireConcurrentFolderReads(t, f, readers)

	// The count is read twice, with a pause between, so a write that landed
	// late cannot hide behind the first read. A window that failed to
	// suppress would show up in one of the two.
	time.Sleep(500 * time.Millisecond)
	written, err = stamps()
	require.NoError(t, err)
	assert.Equal(t, 1, written,
		"a second burst of %d reads inside the ten-minute window must not stamp again", readers)

	time.Sleep(500 * time.Millisecond)
	written, err = stamps()
	require.NoError(t, err)
	assert.Equal(t, 1, written,
		"a second burst of %d reads inside the ten-minute window must not stamp again, "+
			"not even late", readers)

	after := storedFolderView(t, f.client.pool, f.rootID)
	require.NotNil(t, after)
	assert.True(t, after.Equal(*stored),
		"the stamp must not move inside the window: %s then %s", stored, after)
}

// fireConcurrentFolderReads fires n anonymous reads of one folder link at
// once, and waits for all of them.
func fireConcurrentFolderReads(t *testing.T, f *folderShareFixture, n int) {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := http.Get(f.client.server.URL + "/f/" + f.token)
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	close(start)
	wg.Wait()
}

// newFolderShareStampCounter installs a counter for the writes of
// folder_shares.last_viewed_at on one folder's row, and returns a function
// that reads it.
//
// It counts the column's writes, so "at most one stamp per window" is a
// measured fact rather than an inference from a timestamp: a second write
// inside the window would move the value, but two writes inside the same
// clock tick would not, and the timestamp is exactly what is under test.
//
// The counter is a trigger on the test's own temporary database. It is a
// measurement instrument, not a change to the product: it lives in the test's
// database and touches no production code.
//
// The read returns an error rather than failing the test, because it is also
// read from inside require.Eventually's goroutine, where a test failure cannot
// be reported.
func newFolderShareStampCounter(t *testing.T, f *folderShareFixture) func() (int, error) {
	t.Helper()

	_, err := f.client.pool.WriteDB.Exec(`
		CREATE TABLE IF NOT EXISTS qa_folder_share_stamps (n INTEGER NOT NULL DEFAULT 0)`)
	require.NoError(t, err)
	// One row, so the trigger's UPDATE has a row to increment.
	_, err = f.client.pool.WriteDB.Exec(`DELETE FROM qa_folder_share_stamps`)
	require.NoError(t, err)
	_, err = f.client.pool.WriteDB.Exec(`INSERT INTO qa_folder_share_stamps (n) VALUES (0)`)
	require.NoError(t, err)
	// The trigger counts only real stamps: a rotation writes NULL into the
	// column, and a write that matched no row fires nothing at all.
	_, err = f.client.pool.WriteDB.Exec(`
		CREATE TRIGGER IF NOT EXISTS qa_count_folder_share_stamps
		AFTER UPDATE OF last_viewed_at ON folder_shares
		WHEN OLD.folder_id = ` + i64str(f.rootID) + ` AND NEW.last_viewed_at IS NOT NULL
		BEGIN
			UPDATE qa_folder_share_stamps SET n = n + 1;
		END`)
	require.NoError(t, err)

	return func() (int, error) {
		var n int
		err := f.client.pool.ReadDB.QueryRow(
			`SELECT n FROM qa_folder_share_stamps`).Scan(&n)
		return n, err
	}
}
