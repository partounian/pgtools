//go:build integration
// +build integration

package sqltest_test

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v4"
	"github.com/partounian/pgtools/sqltest"
)

var force = flag.Bool("force", false, "Force cleaning the database before starting")

func TestNow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	migration := sqltest.New(t, sqltest.Options{
		Force: *force,
		Path:  "example/testdata/migrations",

		// If we don't use a prefix, the test will be flaky when testing multi packages
		// because there is another TestNow function in example/example_test.go.
		TemporaryDatabasePrefix: "test_internal_",
	})
	conn := migration.Setup(ctx, "") // Using environment variables instead of connString to configure tests.
	var tt time.Time
	if err := conn.QueryRow(ctx, "SELECT NOW();").Scan(&tt); err != nil {
		t.Errorf("cannot execute query: %v", err)
	}
	if tt.IsZero() {
		t.Error("time returned by pgx is zero")
	}
}

func TestPrefixedDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	migration := sqltest.New(t, sqltest.Options{
		Force:                   *force,
		Path:                    "example/testdata/migrations",
		TemporaryDatabasePrefix: "test_must_have_prefix_",
	})
	conn := migration.Setup(ctx, "") // Using environment variables instead of connString to configure tests.
	var got string
	if err := conn.QueryRow(ctx, "SELECT current_database();").Scan(&got); err != nil {
		t.Errorf("cannot get database name: %v", err)
	}
	if want := "test_must_have_prefix_testprefixeddatabase"; want != got {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

var checkMigrationInvalidPath = flag.Bool("check_migration_invalid_path", false, "if true, TestMigrationInvalidPath should fail.")

func TestMigrationInvalidPath(t *testing.T) {
	if *checkMigrationInvalidPath {
		ctx := context.Background()
		migration := sqltest.New(t, sqltest.Options{Force: *force, Path: "testdata/invalid", UseExisting: true})
		migration.Setup(ctx, "")
		return
	}

	args := []string{
		"-test.v",
		"-test.run=TestMigrationInvalidPath",
		"-check_migration_invalid_path",
	}
	if *force {
		args = append(args, "-force")
	}
	out, err := exec.Command(os.Args[0], args...).CombinedOutput()
	if err == nil {
		t.Error("expected command to fail")
	}
	if want := []byte("cannot load migrations: open testdata/invalid: no such file or directory"); !bytes.Contains(out, want) {
		t.Errorf("got %q, wanted %q", out, want)
	}
}

// Check what happens if there is a dirty migration.
var checkMigrationDirty = flag.Bool("check_migration_dirty", false, "if true, TestMigrationDirty should fail.")

func TestMigrationDirty(t *testing.T) {
	if *checkMigrationDirty {
		ctx := context.Background()
		migration := sqltest.New(t, sqltest.Options{Path: "example/testdata/migrations", UseExisting: true})
		migration.Setup(ctx, "")
		return
	}

	// Prepare clean environment.
	ctx := context.Background()
	migration := sqltest.New(t, sqltest.Options{Force: *force, Path: "example/testdata/migrations", UseExisting: true})
	conn := migration.Setup(ctx, "")

	// Check if the migration version matches with the number of migration files.
	entries, err := ioutil.ReadDir("example/testdata/migrations")
	if err != nil {
		t.Errorf("cannot read migrations dir: %v", err)
	}
	var migrations int
	for _, f := range entries {
		if strings.HasSuffix(f.Name(), ".sql") {
			migrations++
		}
	}

	// Let's update the schema_version to make it dirty, and verify we are unable to run the tests.
	if _, err := conn.Exec(context.Background(), "UPDATE schema_version SET version = $1 WHERE version = $2", migrations+1, migrations); err != nil {
		t.Errorf("cannot update migration version: %q", err)
	}

	args := []string{
		"-test.v",
		"-test.run=TestMigrationDirty",
		"-check_migration_dirty",
	}
	if *force {
		args = append(args, "-force")
	}
	out, err := exec.Command(os.Args[0], args...).CombinedOutput()
	if err == nil {
		t.Error("expected command to fail")
	}
	want := []byte(`database is dirty, please fix "schema_version" table manually or try -force`)
	if !bytes.Contains(out, want) {
		t.Errorf("got %q, wanted %q", out, want)
	}

	// Manually reset migration version to be able to test again to after the first 'legit' migration:
	if _, err := conn.Exec(context.Background(), "UPDATE schema_version SET version = $1", migrations); err != nil {
		t.Errorf("cannot update migration version: %q", err)
	}
}

var checkExistingTemporaryDB = flag.Bool("check_existing_temporary_db", false, "if true, ExistingTemporaryDB should fail.")

func TestExistingTemporaryDB(t *testing.T) {
	t.Parallel()
	if *checkExistingTemporaryDB {
		ctx := context.Background()
		migration := sqltest.New(t, sqltest.Options{Path: "example/testdata/migrations"})
		migration.Setup(ctx, "")
		return
	}

	// Prepare clean environment.
	ctx := context.Background()
	conn, err := pgx.Connect(context.Background(), "")
	if err != nil {
		t.Fatalf("connection error: %v", err)
	}

	testDB := sqltest.SQLTestName(t)
	_, err = conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE "%s";`, testDB))
	if err != nil {
		t.Fatalf("cannot create database: %v", err)
	}
	defer func() {
		conn.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s";`, testDB))
	}()

	args := []string{
		"-test.v",
		"-test.run=TestExistingTemporaryDB",
		"-check_existing_temporary_db",
	}
	if *force {
		args = append(args, "-force")
	}
	out, err := exec.Command(os.Args[0], args...).CombinedOutput()
	if err == nil {
		t.Error("expected command to fail")
	}
	want := []byte(`cannot create database: ERROR: database "testexistingtemporarydb" already exists`)
	if !bytes.Contains(out, want) {
		t.Errorf("got %q, wanted %q", out, want)
	}
}

func TestMigrationUninitialized(t *testing.T) {
	t.Parallel()
	defer func() {
		want := "migration must be initialized with sqltest.New()"
		if r := recover(); r == nil || r != want {
			t.Errorf("wanted panic %q, got %v instead", want, r)
		}
	}()
	m := &sqltest.Migration{}
	m.Setup(context.Background(), "")
}

func TestSQLTestName(t *testing.T) {
	t.Parallel()
	want := []string{
		"testsqltestname_foo",
		"testsqltestname_foo_bar",
	}
	var got []string
	t.Run("foo", func(t *testing.T) {
		got = append(got, sqltest.SQLTestName(t))
		t.Run("bar", func(t *testing.T) {
			got = append(got, sqltest.SQLTestName(t))
		})
	})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
