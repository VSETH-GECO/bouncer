package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newOut returns a parent directory and an out directory inside it holding keep.txt.
func newOut(t *testing.T) (parent, out string) {
	t.Helper()
	parent = t.TempDir()
	out = filepath.Join(parent, "gen")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "keep.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	return parent, out
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return false
}

func onlyGen(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "gen" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("parent contains %v, want only gen", names)
	}
}

func TestRegenerate_FailureKeepsOld(t *testing.T) {
	parent, out := newOut(t)
	boom := errors.New("boom")
	err := regenerate(out, func(dir string) error {
		if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
	if !exists(t, filepath.Join(out, "keep.txt")) {
		t.Error("keep.txt was removed by a failed generation")
	}
	if exists(t, filepath.Join(out, "new.txt")) {
		t.Error("new.txt leaked into out after a failed generation")
	}
	onlyGen(t, parent)
}

func TestRegenerate_SuccessReplaces(t *testing.T) {
	parent, out := newOut(t)
	err := regenerate(out, func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(t, filepath.Join(out, "new.txt")) {
		t.Error("new.txt missing after success")
	}
	if exists(t, filepath.Join(out, "keep.txt")) {
		t.Error("keep.txt survived a successful regeneration")
	}
	onlyGen(t, parent)
}
