package db

import "testing"

// Schema inspection failures must surface as errors. Before this, a failed table check read as "absent", so a database
// whose check failed could be treated as empty and migrated without a backup. A closed handle makes every query fail.
func TestSchemaInspectionReturnsQueryErrors(t *testing.T) {
	paths := testPaths(t)
	seedV1Database(t, paths.DB)
	conn := openRawDB(t, paths.DB)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := inspectSchema(conn); err == nil {
		t.Fatal("inspectSchema() error = nil on a closed handle, want the query error")
	}
	if ok, err := tableExists(conn, "schema_migrations"); err == nil {
		t.Fatalf("tableExists() = %v, nil error on a closed handle, want the query error", ok)
	}
	if ok, err := hasUserTables(conn); err == nil {
		t.Fatalf("hasUserTables() = %v, nil error on a closed handle, want the query error", ok)
	}
}

// With a working handle the checks report real answers: absent tables are (false, nil), not an error.
func TestSchemaInspectionReportsAbsentTablesWithoutError(t *testing.T) {
	paths := testPaths(t)
	seedV1Database(t, paths.DB)
	conn := openRawDB(t, paths.DB)

	if ok, err := tableExists(conn, "profile_games"); err != nil || !ok {
		t.Fatalf("tableExists(profile_games) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := tableExists(conn, "no_such_table"); err != nil || ok {
		t.Fatalf("tableExists(no_such_table) = %v, %v; want false, nil", ok, err)
	}
	if ok, err := hasUserTables(conn); err != nil || !ok {
		t.Fatalf("hasUserTables() = %v, %v; want true, nil", ok, err)
	}

	empty := openRawDB(t, testPaths(t).DB)
	if ok, err := hasUserTables(empty); err != nil || ok {
		t.Fatalf("hasUserTables(empty db) = %v, %v; want false, nil", ok, err)
	}
	state, err := inspectSchema(empty)
	if err != nil {
		t.Fatalf("inspectSchema(empty db) error = %v", err)
	}
	if state.hasMigrations || state.hasUserTables || state.version != 0 {
		t.Fatalf("inspectSchema(empty db) = %+v, want no tables and version 0", state)
	}
}
