package db

import "testing"

// shouldCompact runs after the migration commits. A fresh database (no user tables) is never compacted, and a
// migration must fall in (version, target] to count. Migration 2 is the only compact migration.
func TestShouldCompact(t *testing.T) {
	tests := []struct {
		name   string
		state  schemaState
		target int
		want   bool
	}{
		{name: "fresh database has no user tables", state: schemaState{version: 0}, target: 2, want: false},
		{name: "existing v1 database is compacted by v2", state: schemaState{version: 1, hasUserTables: true}, target: 2, want: true},
		{name: "existing database without a version row is compacted", state: schemaState{version: 0, hasUserTables: true}, target: 2, want: true},
		{name: "already at v2 has nothing to drop", state: schemaState{version: 2, hasUserTables: true}, target: 2, want: false},
		{name: "compact migration outside the target range", state: schemaState{version: 1, hasUserTables: true}, target: 1, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldCompact(tc.state, tc.target); got != tc.want {
				t.Fatalf("shouldCompact(%+v, %d) = %v, want %v", tc.state, tc.target, got, tc.want)
			}
		})
	}
}
