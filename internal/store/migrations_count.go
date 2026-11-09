package store

// migrations_count.go — expose the schema migration count for the panel's
// version --json release stamp (phase 14): the release verifier compares
// this against MANIFEST.json, so a bundle built from a stale tree (fewer
// migrations than the announced SHA) is caught before it ships.

// MigrationCount returns how many schema migrations are compiled into this
// binary.
func MigrationCount() int {
	return len(migrations)
}

// MigrationVersions returns the migration version numbers in order (the
// // N: comments above each entry).
func MigrationVersions() []int64 {
	out := make([]int64, 0, len(migrations))
	for i := range migrations {
		out = append(out, int64(i+1))
	}
	return out
}
