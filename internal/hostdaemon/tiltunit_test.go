package hostdaemon

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTiltRunArgs(t *testing.T) {
	env := []string{"PATH=/usr/bin:/bin", "SECRET=a=b", "EMPTY=", "BASH_FUNC_f%%=() { :; }", "=C:"}
	got := TiltRunArgs("/data/_devstack-host/", env, 10300)
	want := []string{
		"--user", "--unit=devstack-tilt", "--collect", "--working-directory=/data/_devstack-host/",
		"-E", "PATH", "-E", "SECRET", "-E", "EMPTY",
		"--", "tilt", "up", "--host", "0.0.0.0", "--port", "10300",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TiltRunArgs() =\n%q\nwant\n%q", got, want)
	}
}

func TestRotateLogKeepsOneOldFile(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "tilt.log")

	if err := rotateLog(log, 4); err != nil {
		t.Fatalf("rotateLog on a missing file: %v", err)
	}

	write := func(s string) {
		if err := os.WriteFile(log, []byte(s), 0644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	write("1234")
	if err := rotateLog(log, 4); err != nil {
		t.Fatalf("rotateLog: %v", err)
	}
	if read(log) != "1234" {
		t.Errorf("a file at the limit must stay in place")
	}
	if _, err := os.Stat(log + ".1"); !os.IsNotExist(err) {
		t.Errorf("a file at the limit must not rotate")
	}

	write("first")
	if err := rotateLog(log, 4); err != nil {
		t.Fatalf("rotateLog: %v", err)
	}
	write("second")
	if err := rotateLog(log, 4); err != nil {
		t.Fatalf("rotateLog: %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("tilt.log must move away when it is over the limit")
	}
	if got := read(log + ".1"); got != "second" {
		t.Errorf("tilt.log.1 = %q, want the newest rotated file %q", got, "second")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("want one old file only; got %d entries", len(entries))
	}
}
