package store

// scans.go — the shared row-scan helpers (REFACTOR-S3, refactor plan §2.3).
// The store used to hand-roll the same two loop shapes at ~35 sites:
//
//	list:  Query → defer Close → for rows.Next() { Scan; append } → rows.Err()
//	one:   QueryRow → Scan → ErrNoRows mapped to ErrNotFound
//
// scanList and scanOne are those two shapes ONCE. dest returns the scan
// destinations of a fresh zero T (pointers into it), so every converted
// site keeps its exact column order and destination types — the SQL
// results and the error mapping are byte-identical to the hand-rolled
// loops. Misses map through the ONE not-found mapper the store already
// had (wrapNotFound → ErrNotFound, models.go); sites whose miss semantics
// differ (default-on-miss getters, map builders, stateful stream
// accumulators) stay hand-rolled on purpose — see REFACTOR-S3-PANEL-REPORT.

// scanList runs query and scans every row into T. dest must return the
// scan destinations for a fresh zero value (pointers into it). An empty
// result is a nil slice — exactly what the hand-rolled loops returned.
func scanList[T any](s *Store, query string, dest func(*T) []any, args ...any) ([]T, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var v T
		if err := rows.Scan(dest(&v)...); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// scanOne is the single-row form of scanList: the first row of query
// scans into T; a miss maps to ErrNotFound, any other error passes
// through — the wrapNotFound contract the hand-rolled getters served.
func scanOne[T any](s *Store, query string, dest func(*T) []any, args ...any) (T, error) {
	var v T
	if err := s.db.QueryRow(query, args...).Scan(dest(&v)...); err != nil {
		return v, wrapNotFound(err)
	}
	return v, nil
}
