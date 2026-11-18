package repository

import (
	"strings"

	"github.com/th0rn0/thornotes/internal/apperror"
)

// Rules shared by both dialects for the anonymous folder-share surface
// (/f/<token>). They live here, beside share.go, for the same reason the
// share-link lifecycle rules do: they are product decisions, not SQL, and two
// engine implementations of "is this path inside the shared tree" would be two
// chances to disagree.

// FolderSharePageSize is the ceiling on each section of one level listing — not
// on the level as a whole.
//
// Two hundred per section, because the two lists have different sort keys
// (folder name, note slug) and merging them into one cursor would buy nothing:
// at the measured ~79 bytes an entry a full section is ~16 KiB, which is a page,
// not a problem. The cap exists because an uncapped flat folder is the whole
// folder on every request, and no amount of `no-store` makes that a good
// anonymous answer.
const FolderSharePageSize = 200

// Bounds on a path relative to the shared root.
//
// The segment limit is the interesting one: twenty levels of a real notes tree is
// already deep, and both bounds turn an absurd path into the same 404 as an
// unknown token instead of a query with a megabyte of LIKE fodder in it.
const (
	maxShareRelPathBytes    = 1024
	maxShareRelPathSegments = 20
)

// NormalizeShareRelPath validates and normalises a path relative to the shared
// root, and answers apperror.ErrNotFound for anything it refuses.
//
// Two properties, both deliberate:
//
//   - The refusal is not-found rather than a 400. The anonymous surface answers
//     one 404 for every failure — unknown token, revoked, expired, malformed
//     path, a folder outside the tree — because a distinguishable error confirms
//     that a token is real. A guard that answered 400 would be the oracle the
//     rest of the surface is built not to be.
//   - It runs before any query. The service also checks that a resolved folder
//     is still under the root, and that check stays; this one means no crafted
//     path can reach a query in the first place, even if a later refactor
//     forgets the second check.
//
// "." and ".." are refused per segment rather than resolved: a notes tree has no
// use for either, and resolving them is how a traversal guard ends up agreeing
// with its adversary.
func NormalizeShareRelPath(rel string) (string, error) {
	if rel == "" {
		return "", nil
	}
	if len(rel) > maxShareRelPathBytes {
		return "", apperror.ErrNotFound
	}
	if strings.ContainsRune(rel, 0) || strings.ContainsRune(rel, '\\') {
		return "", apperror.ErrNotFound
	}
	segments := strings.Split(rel, "/")
	if len(segments) > maxShareRelPathSegments {
		return "", apperror.ErrNotFound
	}
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." {
			return "", apperror.ErrNotFound
		}
	}
	return strings.Join(segments, "/"), nil
}
