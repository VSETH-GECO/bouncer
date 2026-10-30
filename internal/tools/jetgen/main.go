// Command jetgen regenerates the go-jet code under internal/db/gen from the schema fixture plus bouncer's migrations.
// Run it from the module root; it needs Docker.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/jetgen"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
)

func main() {
	out := flag.String("out", "internal/db/gen", "directory to write generated code into")
	flag.Parse()
	log.SetFlags(0)
	if err := run(*out); err != nil {
		log.Printf("jetgen: %v", err)
		os.Exit(1)
	}
}

func run(out string) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	log.Printf("starting %s with the base schema", testdb.Image)
	inst, err := testdb.Run(ctx)
	if err != nil {
		return err
	}
	defer func() {
		log.Print("terminating database")
		tctx, tcancel := context.WithTimeout(context.Background(), time.Minute)
		defer tcancel()
		err = errors.Join(err, inst.Terminate(tctx))
	}()

	log.Print("applying migrations")
	h, err := inst.Open(ctx)
	if err != nil {
		return err
	}
	defer h.Close()
	if err := h.Migrate(ctx, 0); err != nil {
		return err
	}

	log.Printf("generating code into %s", out)
	return regenerate(out, func(dir string) error {
		return jetgen.Generate(ctx, inst.Config.DSN(), dir)
	})
}

// regenerate runs generate against a fresh temporary directory next to out and swaps it in only on success,
// so a failed generation leaves the existing tree untouched.
func regenerate(out string, generate func(dir string) error) error {
	tmp, err := os.MkdirTemp(filepath.Dir(out), ".jetgen-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp) // no-op once the rename has succeeded
	if err := generate(tmp); err != nil {
		return fmt.Errorf("generate: %w", err)
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}
