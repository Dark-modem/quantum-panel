//go:build !legacy

package panel

// Default-build stand-ins for code that only compiles under
// `-tags legacy` (see legacy/README.md). Behavior is identical to a
// build with the tag for supported operations: the legacy backhaul
// migration is a one-shot for pre-split hosts and does nothing on a
// controller that never had the old units.

// migrateLegacyBackhaul is a no-op in default builds; the real
// implementation lives in backhaul_legacy.go under `-tags legacy`.
func migrateLegacyBackhaul() {}
