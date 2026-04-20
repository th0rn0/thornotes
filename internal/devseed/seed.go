// Package devseed populates a fresh thornotes database with a realistic
// mixed-depth folder tree and ~100 notes, under a well-known dev user.
//
// It exists purely for local development and manual testing — it must
// never be used for real users. The welcome/"Getting Started" note
// created by internal/notes/getting_started.go is a completely separate
// flow for first-time real users and is NOT reused here.
//
// Seeding is idempotent: if the dev user already exists, Seed returns
// early without touching anything. This lets operators set
// THORNOTES_SEED_DEV=true permanently without accumulating duplicate data.
package devseed

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository"
)

// DevUsername is the username of the account created by Seed.
const DevUsername = "dev"

// DevPassword is the password assigned to the seeded dev user.
// It satisfies the auth service's 12-character minimum (`minPasswordLen`).
const DevPassword = "devpassword1"

// seedRNGSeed keeps the random generator deterministic so repeated
// seedings into a clean DB produce identical data. The rand.PCG source
// takes two uint64 seeds.
const (
	seedRNGSeed1 uint64 = 0x7468726e6f746573 // "thrnotes"
	seedRNGSeed2 uint64 = 0x64657673656564ff // "devseed\xff"
)

// Stats is returned by Seed so callers can log what was created.
type Stats struct {
	Folders int
	Notes   int
	Skipped bool
}

// folderSpec describes one node in the seed folder tree.
type folderSpec struct {
	name     string
	children []folderSpec
}

// defaultTree is the folder tree Seed creates for the dev user.
// Depths: roots at 1, nested folders at 2. Total = 18 folders.
var defaultTree = []folderSpec{
	{name: "Work", children: []folderSpec{
		{name: "Projects", children: []folderSpec{
			{name: "Alpha"},
			{name: "Beta"},
		}},
		{name: "Meeting Notes"},
		{name: "1on1s"},
	}},
	{name: "Personal", children: []folderSpec{
		{name: "Journal"},
		{name: "Reading"},
		{name: "Recipes"},
	}},
	{name: "Learning", children: []folderSpec{
		{name: "Go"},
		{name: "Kubernetes"},
		{name: "Rust"},
	}},
	{name: "Ideas"},
	{name: "Archive", children: []folderSpec{
		{name: "2024"},
		{name: "2025"},
	}},
}

// topics feeds title and content generation. Each topic pairs with a
// short description used verbatim in the note body so content varies
// across the 100 seeded notes.
var topics = []struct {
	word        string
	description string
}{
	{"retrospective", "A quick retrospective on what worked and what did not."},
	{"roadmap", "Planning where this effort is headed over the next quarter."},
	{"spike", "Time-boxed spike to de-risk an uncertain area."},
	{"brainstorm", "Unfiltered brainstorm — prune later."},
	{"review", "Review notes captured during the walkthrough."},
	{"checklist", "Checklist to follow before shipping."},
	{"design", "Working design doc — still in flux."},
	{"proposal", "Proposal outlining the recommended path."},
	{"incident", "Post-incident write-up and follow-up actions."},
	{"onboarding", "Notes for someone new joining this area."},
	{"recipe", "A simple weeknight recipe worth keeping."},
	{"summary", "One-page summary distilled from the longer source."},
	{"workflow", "Workflow that has been reliable so far."},
	{"learning", "Something learned worth writing down."},
	{"question", "Open question to follow up on later."},
	{"snippet", "Reusable snippet — copy-paste friendly."},
	{"outline", "Outline to flesh out next time."},
	{"scratchpad", "Scratchpad — expect rough edges."},
}

// tagPool is the fixed set of tags Seed draws from. Keeping it small
// ensures tags actually collide across notes, which makes the tag
// browser more useful while exercising the app.
var tagPool = []string{"draft", "reference", "todo", "idea", "meeting", "learning"}

// Seed creates the dev user, folder tree, and ~100 notes. It is safe to
// call on every startup: if the dev user already exists Seed returns
// Stats{Skipped: true} without doing anything.
//
// It uses the public Service APIs (Register, CreateFolder, CreateNote,
// UpdateNoteContent) so the on-disk files, DB rows, and reconciliation
// invariants all stay in sync.
func Seed(
	ctx context.Context,
	authSvc *auth.Service,
	notesSvc *notes.Service,
	users repository.UserRepository,
) (Stats, error) {
	// Idempotency check: if the dev user already exists, bail out.
	existing, err := users.GetByUsername(ctx, DevUsername)
	if err != nil && !errors.Is(err, apperror.ErrNotFound) {
		return Stats{}, fmt.Errorf("devseed: look up dev user: %w", err)
	}
	if existing != nil {
		return Stats{Skipped: true}, nil
	}

	user, err := authSvc.Register(ctx, DevUsername, DevPassword)
	if err != nil {
		return Stats{}, fmt.Errorf("devseed: register dev user: %w", err)
	}

	// Deterministic RNG — re-runs against a clean DB produce the same
	// tree and note distribution.
	rng := rand.New(rand.NewPCG(seedRNGSeed1, seedRNGSeed2))

	// Create folders depth-first and collect the flat list of IDs.
	var folderIDs []int64
	folderCount, err := createTree(ctx, notesSvc, user.ID, user.UUID, nil, defaultTree, &folderIDs)
	if err != nil {
		return Stats{}, fmt.Errorf("devseed: create folders: %w", err)
	}

	// Target: ~100 notes. Put ~8 at the root, distribute the rest across
	// the folders. Using a deterministic counter per folder ID keeps
	// titles unique within each folder (required by the unique index
	// on (folder_id, slug)).
	const totalNotes = 100
	const rootNotes = 8
	counters := make(map[int64]int, len(folderIDs)+1)

	noteCount := 0
	for i := 0; i < rootNotes; i++ {
		if err := createSeedNote(ctx, notesSvc, user.ID, user.UUID, nil, rng, counters, 0); err != nil {
			return Stats{}, fmt.Errorf("devseed: create root note %d: %w", i, err)
		}
		noteCount++
	}

	for i := rootNotes; i < totalNotes; i++ {
		// Pick a folder deterministically but spread across the tree.
		folderID := folderIDs[rng.IntN(len(folderIDs))]
		if err := createSeedNote(ctx, notesSvc, user.ID, user.UUID, &folderID, rng, counters, folderID); err != nil {
			return Stats{}, fmt.Errorf("devseed: create note %d in folder %d: %w", i, folderID, err)
		}
		noteCount++
	}

	return Stats{Folders: folderCount, Notes: noteCount}, nil
}

// createTree walks the folder spec depth-first, creating each folder
// and appending its ID to ids. Returns the total number of folders
// created.
func createTree(
	ctx context.Context,
	notesSvc *notes.Service,
	userID int64,
	userUUID string,
	parentID *int64,
	specs []folderSpec,
	ids *[]int64,
) (int, error) {
	count := 0
	for _, spec := range specs {
		folder, err := notesSvc.CreateFolder(ctx, userID, userUUID, parentID, spec.name)
		if err != nil {
			return count, fmt.Errorf("create folder %q: %w", spec.name, err)
		}
		*ids = append(*ids, folder.ID)
		count++

		if len(spec.children) > 0 {
			fid := folder.ID
			childCount, err := createTree(ctx, notesSvc, userID, userUUID, &fid, spec.children, ids)
			if err != nil {
				return count + childCount, err
			}
			count += childCount
		}
	}
	return count, nil
}

// createSeedNote creates a single note with a unique-within-folder title
// and a short markdown body. counterKey is the folder ID (or 0 for root)
// used to increment per-folder counters so titles stay unique.
func createSeedNote(
	ctx context.Context,
	notesSvc *notes.Service,
	userID int64,
	userUUID string,
	folderID *int64,
	rng *rand.Rand,
	counters map[int64]int,
	counterKey int64,
) error {
	topic := topics[rng.IntN(len(topics))]
	counters[counterKey]++
	n := counters[counterKey]

	// Title like "Scratchpad 03" — deterministic counter makes collisions
	// impossible within a single folder, which is what the DB requires.
	title := fmt.Sprintf("%s %02d", capitalize(topic.word), n)
	tags := pickTags(rng)

	note, err := notesSvc.CreateNote(ctx, userID, userUUID, folderID, title, tags)
	if err != nil {
		return fmt.Errorf("create note %q: %w", title, err)
	}

	body := buildBody(title, topic.description, tags, rng)
	if _, err := notesSvc.UpdateNoteContent(ctx, userID, note.ID, body, note.ContentHash); err != nil {
		return fmt.Errorf("write content for %q: %w", title, err)
	}
	return nil
}

// pickTags returns 0–3 tags drawn from tagPool. About one in four notes
// gets no tags at all so the UI has untagged notes too.
func pickTags(rng *rand.Rand) []string {
	// 25% untagged, 25% one tag, 25% two tags, 25% three tags.
	n := rng.IntN(4)
	if n == 0 {
		return nil
	}
	// Pick n distinct tags from the pool.
	pool := make([]string, len(tagPool))
	copy(pool, tagPool)
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	return pool[:n]
}

// buildBody returns a short markdown body for a seeded note.
func buildBody(title, description string, tags []string, rng *rand.Rand) string {
	bullets := []string{
		"- First bullet worth revisiting.",
		"- Second thought captured here.",
		"- Open question: does this still apply?",
		"- Link back to related work when it lands.",
		"- Follow up next week if nothing changes.",
	}
	// 2–4 bullets, drawn deterministically from the pool.
	count := 2 + rng.IntN(3)
	rng.Shuffle(len(bullets), func(i, j int) { bullets[i], bullets[j] = bullets[j], bullets[i] })
	picked := bullets[:count]

	body := "# " + title + "\n\n" + description + "\n\n"
	for _, b := range picked {
		body += b + "\n"
	}
	if len(tags) > 0 {
		body += "\n_Tags: "
		for i, t := range tags {
			if i > 0 {
				body += ", "
			}
			body += t
		}
		body += "_\n"
	}
	return body
}

// capitalize uppercases the first ASCII letter of s. It avoids the
// strings package to keep this file dependency-free and predictable.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 32
	}
	return string(b)
}
