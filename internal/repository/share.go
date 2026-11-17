package repository

import (
	"time"
)

// Share-link lifecycle helpers shared by both dialects.
//
// These live in the repository package rather than in each dialect because the
// rule they encode is a product decision, not a SQL one: two engine
// implementations of "when is a share link dead" or "how often may a view be
// recorded" would be two chances to disagree with each other.

// ShareTokenPrefixLen is how much of a share token an enumeration endpoint may
// reveal. The token is 32 hex characters (128 bits) and is only ever compared
// for equality, so eight characters — 32 bits — is enough to match a row against
// a URL the owner still has in front of them and nowhere near enough to guess or
// replay one.
const ShareTokenPrefixLen = 8

// RedactShareToken reduces a live share token to a display prefix.
//
// It is the only way a token should reach a list response: GET /api/v1/shares
// exists so a user can find and revoke the links they handed out, and answering
// one authenticated read with every working bearer capability would be a bulk
// export of the shares themselves. Anything that needs a usable token still gets
// it from the note that owns it.
//
// The ellipsis is part of the value so a client cannot mistake the prefix for a
// whole token. A token shorter than the prefix is never revealed: slicing it
// would return the entire secret.
func RedactShareToken(token string) string {
	if len(token) <= ShareTokenPrefixLen {
		return "…"
	}
	return token[:ShareTokenPrefixLen] + "…"
}

// ShareViewDue reports whether a view at now should be written, given the last
// view already stored for that link.
//
// This is the bound on the touch: a share link is a read-heavy, unauthenticated
// surface, so a write per request would turn every view into a database write
// and give anyone holding a URL a cheap way to grow the table. A nil previous
// (never viewed) is always due. A future previous — clock skew between the app
// and the database — is never due, so skew suppresses the write instead of
// stamping every request.
func ShareViewDue(previous *time.Time, now time.Time, window time.Duration) bool {
	if window <= 0 {
		return true
	}
	if previous == nil {
		return true
	}
	return now.Sub(*previous) >= window
}
