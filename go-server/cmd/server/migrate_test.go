package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := runMigrate(args, func(string) string { return "" }, &out, &errb)
	return code, out.String(), errb.String()
}

func TestMigrateCommands(t *testing.T) {
	db := filepath.Join(t.TempDir(), "m.sqlite")

	if code, _, errs := run("status", "-db", db); code != 1 || !strings.Contains(errs, "не найдена") {
		t.Fatalf("status на отсутствующей БД: код %d, %q", code, errs)
	}
	if code, out, errs := run("up", "-db", db); code != 0 || !strings.Contains(out, "applied") {
		t.Fatalf("up: код %d, %q %q", code, out, errs)
	}
	if code, out, _ := run("status", "-db", db); code != 0 || !strings.Contains(out, "00001") {
		t.Fatalf("status: код %d, %q", code, out)
	}
	if code, _, errs := run("down", "-db", db); code != 1 || !strings.Contains(errs, "baseline") {
		t.Fatalf("down ниже baseline: код %d, %q", code, errs)
	}
	if code, _, _ := run("down", "1", "-db", db); code != 1 {
		t.Fatalf("down 1 ниже baseline должен отказать, код %d", code)
	}
	if code, _, _ := run("bogus"); code != 2 {
		t.Fatalf("неизвестная команда: код %d", code)
	}
	if code, _, _ := run(); code != 2 {
		t.Fatalf("без команды: код %d", code)
	}
}
