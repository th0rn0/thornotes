package mysql

import "strings"

// isUniqueConstraint reports whether err is a MySQL duplicate-key error (1062).
func isUniqueConstraint(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "Error 1062") ||
		strings.Contains(err.Error(), "Duplicate entry") ||
		strings.Contains(err.Error(), "duplicate key"))
}
