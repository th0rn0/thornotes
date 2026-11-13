package handler_test

// Ticket 633 §2: the read-only report of links that do not resolve. The
// endpoint must answer what is broken, never repair it, and never need the link
// index to have been built.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandler_BrokenLinks_ReportsDanglingLinks(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	targetID := createNoteInFolder(t, c, "Renaming Notes", nil, "")
	srcID := createNoteInFolder(t, c, "Source Page", nil,
		"see [[Renaming Note#Why|the old page]], [[Renaming Note]], and [[Nowhere At All]]")

	resp := c.do(t, http.MethodGet, "/api/v1/links/broken", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var report struct {
		Notes          int              `json:"notes"`
		Links          int              `json:"links"`
		Broken         []map[string]any `json:"broken"`
		RepairRequires string           `json:"repair_requires"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&report))

	// A new account also gets its getting-started note, so the report is
	// filtered to the note this test wrote rather than assumed to be the whole
	// vault.
	assert.GreaterOrEqual(t, report.Notes, 2)

	var byTarget = map[string]map[string]any{}
	for _, b := range report.Broken {
		if int64(b["note_id"].(float64)) != srcID {
			continue
		}
		byTarget[b["target"].(string)] = b
	}
	require.Len(t, byTarget, 2)

	near, ok := byTarget["Renaming Note"]
	require.True(t, ok, "the link that lost its target is reported")
	assert.Equal(t, "Source Page", near["note_title"])
	assert.Equal(t, "Why", near["anchor"])
	assert.Equal(t, "the old page", near["link_text"])
	assert.Equal(t, false, near["ambiguous"])
	hint, _ := near["near_match"].(string)
	assert.Contains(t, hint, "Renaming Notes")

	far, ok := byTarget["Nowhere At All"]
	require.True(t, ok)
	_, hasHint := far["near_match"]
	assert.False(t, hasHint, "an unrelated target gets no hint")

	// The report explains why it does not repair anything, so the reader is not
	// left thinking a fix was skipped by accident.
	assert.Contains(t, report.RepairRequires, "title history")

	// Read-only: the note's body is exactly what was written.
	body := getNoteContent(t, c, near["note_id"].(float64))
	assert.Contains(t, body, "[[Renaming Note#Why|the old page]]",
		"the report must never rewrite the link it reports")
	assert.NotEqual(t, float64(0), float64(near["note_id"].(float64)))
	assert.NotZero(t, targetID)
}

// getNoteContent fetches a note and returns its body.
func getNoteContent(t *testing.T, c *testClient, noteID float64) string {
	t.Helper()
	resp := c.do(t, http.MethodGet, "/api/v1/notes/"+strconv.FormatInt(int64(noteID), 10), nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var note map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&note))
	content, _ := note["content"].(string)
	return content
}
