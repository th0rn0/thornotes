package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// H-05: PATCH /api/v1/notes/:id carrying both content and metadata (tags/title)
// must persist both, while content-only and metadata-only clients keep existing
// behaviour.
// =============================================================================

func TestP0_H05_PatchNote_ContentAndTags_PersistsBoth(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Combined Patch Note")

	// Fetch initial content_hash.
	getResp1 := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(id), nil)
	var initial map[string]any
	require.NoError(t, json.NewDecoder(getResp1.Body).Decode(&initial))
	getResp1.Body.Close()
	initialHash := initial["content_hash"].(string)

	// Single PATCH carrying content, content_hash, and tags (plus optional title).
	patchResp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(id), map[string]any{
		"content":      "# Combined Body\n\nSaved together with tags.",
		"content_hash": initialHash,
		"tags":         []string{"p0", "combined"},
	})
	defer patchResp.Body.Close()
	require.Equal(t, http.StatusOK, patchResp.StatusCode)

	var patchData map[string]any
	require.NoError(t, json.NewDecoder(patchResp.Body).Decode(&patchData))
	newHash, ok := patchData["content_hash"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, newHash)
	assert.NotEqual(t, initialHash, newHash)

	// Verify both content and tags persisted in the note.
	getResp2 := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(id), nil)
	defer getResp2.Body.Close()
	require.Equal(t, http.StatusOK, getResp2.StatusCode)

	var updated map[string]any
	require.NoError(t, json.NewDecoder(getResp2.Body).Decode(&updated))
	assert.Equal(t, "# Combined Body\n\nSaved together with tags.", updated["content"])
	assert.Equal(t, newHash, updated["content_hash"])
	assert.Equal(t, []any{"p0", "combined"}, updated["tags"])

	// Subsequent content-only PATCH must NOT wipe existing tags.
	patchContentOnly := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(id), map[string]any{
		"content":      "# Second Save\n\nContent-only autosave.",
		"content_hash": newHash,
	})
	defer patchContentOnly.Body.Close()
	require.Equal(t, http.StatusOK, patchContentOnly.StatusCode)

	var patchData2 map[string]any
	require.NoError(t, json.NewDecoder(patchContentOnly.Body).Decode(&patchData2))
	hash2 := patchData2["content_hash"].(string)

	getResp3 := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(id), nil)
	defer getResp3.Body.Close()
	var afterContentOnly map[string]any
	require.NoError(t, json.NewDecoder(getResp3.Body).Decode(&afterContentOnly))
	assert.Equal(t, "# Second Save\n\nContent-only autosave.", afterContentOnly["content"])
	assert.Equal(t, hash2, afterContentOnly["content_hash"])
	assert.Equal(t, []any{"p0", "combined"}, afterContentOnly["tags"], "content-only PATCH must preserve existing tags")

	// Subsequent metadata-only PATCH (tags + title) must keep existing behaviour and return message/slug/title.
	patchMetaOnly := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(id), map[string]any{
		"title": "Renamed Combined Note",
		"tags":  []string{"meta-only"},
	})
	defer patchMetaOnly.Body.Close()
	require.Equal(t, http.StatusOK, patchMetaOnly.StatusCode)

	var metaResp map[string]any
	require.NoError(t, json.NewDecoder(patchMetaOnly.Body).Decode(&metaResp))
	assert.Equal(t, "updated", metaResp["message"])
	assert.Equal(t, "renamed-combined-note", metaResp["slug"])
	assert.Equal(t, "Renamed Combined Note", metaResp["title"])

	getResp4 := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(id), nil)
	defer getResp4.Body.Close()
	var afterMetaOnly map[string]any
	require.NoError(t, json.NewDecoder(getResp4.Body).Decode(&afterMetaOnly))
	assert.Equal(t, "# Second Save\n\nContent-only autosave.", afterMetaOnly["content"])
	assert.Equal(t, "Renamed Combined Note", afterMetaOnly["title"])
	assert.Equal(t, []any{"meta-only"}, afterMetaOnly["tags"])
}

func TestP0_H05_PatchNote_ContentTitleAndTags_PersistsAllAndReturnsHashSlugTitle(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Initial Title")

	getResp1 := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(id), nil)
	var initial map[string]any
	require.NoError(t, json.NewDecoder(getResp1.Body).Decode(&initial))
	getResp1.Body.Close()
	initialHash := initial["content_hash"].(string)

	patchResp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(id), map[string]any{
		"content":      "Updated content with title and tags",
		"content_hash": initialHash,
		"title":        "Brand New Title",
		"tags":         []string{"t1", "t2"},
	})
	defer patchResp.Body.Close()
	require.Equal(t, http.StatusOK, patchResp.StatusCode)

	var patchData map[string]any
	require.NoError(t, json.NewDecoder(patchResp.Body).Decode(&patchData))
	assert.NotEmpty(t, patchData["content_hash"])
	assert.Equal(t, "brand-new-title", patchData["slug"])
	assert.Equal(t, "Brand New Title", patchData["title"])

	getResp2 := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(id), nil)
	defer getResp2.Body.Close()
	var updated map[string]any
	require.NoError(t, json.NewDecoder(getResp2.Body).Decode(&updated))
	assert.Equal(t, "Updated content with title and tags", updated["content"])
	assert.Equal(t, "Brand New Title", updated["title"])
	assert.Equal(t, "brand-new-title", updated["slug"])
	assert.Equal(t, []any{"t1", "t2"}, updated["tags"])
}

func TestP0_H05_PatchNote_StaleContentHash_DoesNotMutateTagsOrTitle(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Conflict Atomic Note")

	// Set initial tags.
	initTagsResp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(id), map[string]any{
		"tags": []string{"original-tag"},
	})
	initTagsResp.Body.Close()
	require.Equal(t, http.StatusOK, initTagsResp.StatusCode)

	// Combined PATCH with a stale content_hash must return 409 and NOT overwrite tags or title.
	conflictResp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(id), map[string]any{
		"content":      "should be rejected",
		"content_hash": "0000000000000000000000000000000000000000000000000000000000000000",
		"title":        "Should Not Apply",
		"tags":         []string{"clobbered-tag"},
	})
	defer conflictResp.Body.Close()
	assert.Equal(t, http.StatusConflict, conflictResp.StatusCode)

	getResp := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(id), nil)
	defer getResp.Body.Close()
	var got map[string]any
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&got))
	assert.Equal(t, "Conflict Atomic Note", got["title"])
	assert.Equal(t, []any{"original-tag"}, got["tags"])
}

func TestP0_H05_PatchNote_CombinedMetadataCollision_DoesNotPartiallyMutateContentOrTarget(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// Create existing target note and set its content.
	targetID := createNoteHelper(t, c, "Existing Target")
	targetGet1 := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(targetID), nil)
	var targetInit map[string]any
	require.NoError(t, json.NewDecoder(targetGet1.Body).Decode(&targetInit))
	targetGet1.Body.Close()
	targetPatch := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(targetID), map[string]any{
		"content":      "# Target Content\n\nMust stay intact.",
		"content_hash": targetInit["content_hash"].(string),
		"tags":         []string{"target-tag"},
	})
	targetPatch.Body.Close()
	require.Equal(t, http.StatusOK, targetPatch.StatusCode)

	// Create source note and set its initial content + tags.
	sourceID := createNoteHelper(t, c, "Source Note")
	sourceGet1 := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(sourceID), nil)
	var sourceInit map[string]any
	require.NoError(t, json.NewDecoder(sourceGet1.Body).Decode(&sourceInit))
	sourceGet1.Body.Close()
	sourcePatch1 := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(sourceID), map[string]any{
		"content":      "# Original Source Content",
		"content_hash": sourceInit["content_hash"].(string),
		"tags":         []string{"source-tag"},
	})
	var sourcePatch1Data map[string]any
	require.NoError(t, json.NewDecoder(sourcePatch1.Body).Decode(&sourcePatch1Data))
	sourcePatch1.Body.Close()
	require.Equal(t, http.StatusOK, sourcePatch1.StatusCode)
	origSourceHash := sourcePatch1Data["content_hash"].(string)

	// Combined PATCH on source: valid content_hash + new content + colliding title "Existing Target" + new tags.
	// Must fail with 409 Conflict AND leave source's content, content_hash, title, and tags (plus target) completely unchanged!
	collisionResp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(sourceID), map[string]any{
		"content":      "# Partially Mutated Content Should Not Persist",
		"content_hash": origSourceHash,
		"title":        "Existing Target",
		"tags":         []string{"mutated-tag"},
	})
	defer collisionResp.Body.Close()
	assert.Equal(t, http.StatusConflict, collisionResp.StatusCode)

	// Verify source note was not partially mutated.
	sourceGet2 := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(sourceID), nil)
	defer sourceGet2.Body.Close()
	require.Equal(t, http.StatusOK, sourceGet2.StatusCode)
	var sourceAfter map[string]any
	require.NoError(t, json.NewDecoder(sourceGet2.Body).Decode(&sourceAfter))
	assert.Equal(t, "Source Note", sourceAfter["title"])
	assert.Equal(t, "source-note", sourceAfter["slug"])
	assert.Equal(t, "# Original Source Content", sourceAfter["content"])
	assert.Equal(t, origSourceHash, sourceAfter["content_hash"])
	assert.Equal(t, []any{"source-tag"}, sourceAfter["tags"])

	// Verify target note was not clobbered.
	targetGet2 := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(targetID), nil)
	defer targetGet2.Body.Close()
	require.Equal(t, http.StatusOK, targetGet2.StatusCode)
	var targetAfter map[string]any
	require.NoError(t, json.NewDecoder(targetGet2.Body).Decode(&targetAfter))
	assert.Equal(t, "Existing Target", targetAfter["title"])
	assert.Equal(t, "# Target Content\n\nMust stay intact.", targetAfter["content"])
	assert.Equal(t, []any{"target-tag"}, targetAfter["tags"])
}
