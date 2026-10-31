package jetgen_test

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/VSETH-GECO/bouncer/internal/jetgen"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// TestGeneratedCodeIsCurrent generates the go-jet code from the shared, migrated test database and diffs it against
// the committed tree.
func TestGeneratedCodeIsCurrent(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	if err := jetgen.Generate(ctx, testdb.SharedConfig(t).DSN(), tmp); err != nil {
		t.Fatal(err)
	}
	committed := filepath.Join(thisDir(t), "..", "db", "gen")
	if diffs := compareTrees(t, tmp, committed); len(diffs) > 0 {
		t.Fatalf("generated code is out of date, run `go run ./internal/tools/jetgen`:\n%s", strings.Join(diffs, "\n"))
	}
}

func thisDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source file")
	}
	return filepath.Dir(file)
}

// compareTrees lists relative paths present in only one tree or whose bytes differ.
func compareTrees(t *testing.T, generated, committed string) []string {
	t.Helper()
	gen, com := readTree(t, generated), readTree(t, committed)
	var diffs []string
	for p, g := range gen {
		c, ok := com[p]
		switch {
		case !ok:
			diffs = append(diffs, "missing from committed tree: "+p)
		case !bytes.Equal(g, c):
			diffs = append(diffs, "content differs: "+p)
		}
	}
	for p := range com {
		if _, ok := gen[p]; !ok {
			diffs = append(diffs, "only in committed tree: "+p)
		}
	}
	sort.Strings(diffs)
	return diffs
}

func readTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		files[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading %s: %v", root, err)
	}
	return files
}
