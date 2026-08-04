package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/migrate"
)

// fakeMigrateBinary writes a program that exits with code.
func fakeMigrateBinary(t *testing.T, code string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("this test runs a shell script")
	}
	path := filepath.Join(t.TempDir(), "devstack")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit "+code+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

// `upgrade` runs the migration in another process, so the exit status is the
// only thing it reads. A refusal changed no file and stopped no service, and a
// write that failed did neither, so the two can not share one status.
func TestUpgradeReadsARefusalOutOfTheExitStatus(t *testing.T) {
	err := runMigration(fakeMigrateBinary(t, "3"))
	if !errors.Is(err, migrate.ErrRefused) {
		t.Fatalf("runMigration() = %v, which `upgrade` reads as a failed write", err)
	}
}

func TestUpgradeReadsAnyOtherExitStatusAsAFailure(t *testing.T) {
	err := runMigration(fakeMigrateBinary(t, "1"))
	if err == nil {
		t.Fatal("runMigration() returned no error")
	}
	if errors.Is(err, migrate.ErrRefused) {
		t.Fatalf("runMigration() = %v, which `upgrade` reads as a refusal", err)
	}
	if !strings.Contains(err.Error(), "devstack migrate") {
		t.Errorf("the failure never names the command that prints the error: %v", err)
	}
}

// The status the binary exits with is the status `upgrade` compares against, so
// the two have to be the one constant.
func TestARefusalExitsWithTheStatusUpgradeReads(t *testing.T) {
	if exitMigrateRefused == 1 || exitMigrateRefused == 0 {
		t.Fatalf("exitMigrateRefused is %d, which no failure can be told apart from", exitMigrateRefused)
	}
}
