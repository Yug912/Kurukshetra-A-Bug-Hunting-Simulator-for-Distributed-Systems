package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// exportRuns writes a static copy of the runs API for the viewer's demo build
// (no kuru serve needed): <out>/runs.json (the GET /api/runs list) and every
// servable file of each run under <out>/runs/<scenario>/<run>/. only, if not
// empty, restricts the export to those run IDs, in that order.
func exportRuns(root, out string, only []string) ([]RunEntry, error) {
	all, err := listRuns(root)
	if err != nil {
		return nil, err
	}
	runs := all
	if len(only) > 0 {
		runs = runs[:0:0]
		for _, id := range only {
			i := slices.IndexFunc(all, func(e RunEntry) bool { return e.ID == id })
			if i < 0 {
				return nil, fmt.Errorf("export: run %q not found under %s", id, root)
			}
			runs = append(runs, all[i])
		}
	}
	if err := os.RemoveAll(filepath.Join(out, "runs")); err != nil {
		return nil, err
	}
	for _, e := range runs {
		src := filepath.Join(root, filepath.FromSlash(e.ID))
		dst := filepath.Join(out, "runs", filepath.FromSlash(e.ID))
		if err := copyServable(src, dst); err != nil {
			return nil, fmt.Errorf("export %s: %w", e.ID, err)
		}
	}
	b, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		return nil, err
	}
	return runs, os.WriteFile(filepath.Join(out, "runs.json"), append(b, '\n'), 0o644)
}

// copyServable copies the files kuru serve would serve for one run.
func copyServable(src, dst string) error {
	if err := os.MkdirAll(filepath.Join(dst, "logs"), 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(runFiles))
	for n := range runFiles {
		names = append(names, n)
	}
	if logs, err := os.ReadDir(filepath.Join(src, "logs")); err == nil {
		for _, l := range logs {
			if n := "logs/" + l.Name(); allowedFile(n) {
				names = append(names, n)
			}
		}
	}
	for _, n := range names {
		if err := copyFile(filepath.Join(src, n), filepath.Join(dst, n)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil // never follow symlinks out of the runs dir
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// splitIDs parses a comma-separated run ID list.
func splitIDs(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
