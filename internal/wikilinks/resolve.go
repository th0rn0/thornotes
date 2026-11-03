package wikilinks

// Resolver resolves link targets against an in-memory set of notes and folders,
// following note 323 §5 with no fuzzy/edit-distance matching. It is the pure
// core that phase-1b will back with repository queries.
type Resolver struct {
	notes        []NoteRef
	titleKey     map[int64]string   // note id -> NormalizeKey(title)
	bySlug       map[string][]int64 // slug -> note ids
	folderParent map[int64]int64    // folder id -> parent folder id (0 = root)
}

func NewResolver(notes []NoteRef, folders []FolderRef) *Resolver {
	r := &Resolver{
		notes:        notes,
		titleKey:     make(map[int64]string, len(notes)),
		bySlug:       make(map[string][]int64, len(notes)),
		folderParent: make(map[int64]int64, len(folders)),
	}
	for _, n := range notes {
		r.titleKey[n.ID] = NormalizeKey(n.Title)
		r.bySlug[n.Slug] = append(r.bySlug[n.Slug], n.ID)
	}
	for _, f := range folders {
		r.folderParent[f.ID] = locKey(f.ParentID)
	}
	return r
}

// Resolve resolves target from srcFolderID. Resolution order (note 323 §5):
//
//	empty -> unresolved
//	exactly one candidate -> that candidate
//	otherwise -> nearest by common-folder-ancestor distance (a same-folder
//	             candidate is distance 0 and wins); ties -> ambiguous
func (r *Resolver) Resolve(srcFolderID *int64, target string) Resolution {
	res := Resolution{Target: target}
	res.Key = NormalizeKey(target)
	res.Slug = Slugify(target)
	res.EdgeKey = EdgeKey(target)

	// Candidates match by slug OR title_key (note 323 §5), deduped by id.
	ids := make([]int64, 0, 4)
	seen := make(map[int64]struct{}, 4)
	for _, id := range r.bySlug[res.Slug] {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for id, tk := range r.titleKey {
		if tk == res.Key {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}

	if len(ids) == 0 {
		return res // unresolved, not ambiguous
	}
	if len(ids) == 1 {
		res.NoteID = ids[0]
		res.Resolved = true
		return res
	}

	srcChain := r.folderChain(srcFolderID)
	minDist := -1
	dists := make(map[int64]int, len(ids))
	for _, id := range ids {
		d := ncaDistance(srcChain, r.folderChain(r.noteFolder(id)))
		dists[id] = d
		if minDist < 0 || d < minDist {
			minDist = d
		}
	}
	var nearest []int64
	for _, id := range ids {
		if dists[id] == minDist {
			nearest = append(nearest, id)
		}
	}
	if len(nearest) == 1 {
		res.NoteID = nearest[0]
		res.Resolved = true
		return res
	}
	res.Ambiguous = true
	res.Candidates = nearest
	return res
}

func (r *Resolver) noteFolder(id int64) *int64 {
	for _, n := range r.notes {
		if n.ID == id {
			return n.FolderID
		}
	}
	return nil
}

// folderChain returns the ancestor chain of loc up to the root, inclusive,
// where the root is encoded as 0. The first element is loc itself.
func (r *Resolver) folderChain(loc *int64) []int64 {
	chain := make([]int64, 0, 4)
	seen := make(map[int64]struct{}, 4)
	cur := locKey(loc)
	for {
		if _, ok := seen[cur]; ok {
			break // cycle guard (defensive; input is well-formed)
		}
		seen[cur] = struct{}{}
		chain = append(chain, cur)
		if cur == 0 {
			break
		}
		parent, ok := r.folderParent[cur]
		if !ok {
			break // unknown folder: treat as a leaf
		}
		cur = parent
	}
	return chain
}

// ncaDistance returns (steps up from a to the nearest common ancestor) +
// (steps up from b to the nearest common ancestor). Both chains end at 0.
func ncaDistance(a, b []int64) int {
	idx := make(map[int64]int, len(a))
	for i, v := range a {
		if _, ok := idx[v]; !ok {
			idx[v] = i
		}
	}
	for j, v := range b {
		if i, ok := idx[v]; ok {
			return i + j
		}
	}
	return (len(a) - 1) + (len(b) - 1)
}

func locKey(l *int64) int64 {
	if l == nil {
		return 0
	}
	return *l
}
