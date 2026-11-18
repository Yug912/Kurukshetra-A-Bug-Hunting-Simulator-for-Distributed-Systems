package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kurukshetra/pkg/core"
)

// serveFixture builds a runs dir with the sample as sample/42 and a second run, plus a web dir.
func serveFixture(t *testing.T) (runs, web string) {
	t.Helper()
	runs = t.TempDir()
	sample := sampleCopy(t)
	if err := os.MkdirAll(filepath.Join(runs, "sample"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(sample, filepath.Join(runs, "sample", "42")); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(runs, "toykv-stale-read", "7")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	fid := 0.97
	must(t, core.WriteMeta(other, core.Meta{Scenario: "toykv-stale-read", Seed: 7, StartedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), ReplayOf: "runs/toykv-stale-read/6"}))
	must(t, core.WriteResult(other, core.Result{Seed: 7, Scenario: "toykv-stale-read", Verdict: core.VerdictPass, Fidelity: &fid}))
	must(t, os.WriteFile(filepath.Join(runs, "toykv-stale-read", "summary.csv"), []byte("seed,verdict\n1,FAIL\n2,PASS\n"), 0o644))
	// Noise that must not be listed: a hidden shrink temp dir and a folder without meta/result.
	must(t, os.MkdirAll(filepath.Join(runs, "sample", ".shrink-123", "trial-001"), 0o755))
	must(t, os.MkdirAll(filepath.Join(runs, "sample", "empty"), 0o755))
	// A secret outside the runs dir, and a symlink to it inside a run.
	secret := filepath.Join(t.TempDir(), "secret.json")
	must(t, os.WriteFile(secret, []byte(`{"secret":true}`), 0o644))
	if err := os.Symlink(secret, filepath.Join(runs, "sample", "42", "minimal.json")); err != nil {
		t.Fatal(err)
	}
	web = t.TempDir()
	must(t, os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>viewer</html>"), 0o644))
	must(t, os.MkdirAll(filepath.Join(web, "assets"), 0o755))
	must(t, os.WriteFile(filepath.Join(web, "assets", "app.js"), []byte("console.log(1)"), 0o644))
	return runs, web
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, h http.Handler, url string, hdr ...string) (int, string, http.Header) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b), rec.Header()
}

func TestServeListRuns(t *testing.T) {
	runs, web := serveFixture(t)
	h := newServeHandler(runs, web)
	code, body, hdr := get(t, h, "/api/runs")
	if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "application/json") {
		t.Fatalf("%d %s", code, body)
	}
	var list []RunEntry
	must(t, json.Unmarshal([]byte(body), &list))
	if len(list) != 2 {
		t.Fatalf("%s", body)
	}
	// Newest first: the 2026-05-01 run before the sample (2026-01-01).
	a, b := list[0], list[1]
	if a.ID != "toykv-stale-read/7" || a.Verdict != "PASS" || *a.Fidelity != 0.97 || a.ReplayOf == "" || *a.Seed != 7 {
		t.Fatalf("%+v", a)
	}
	if b.ID != "sample/42" || b.Scenario != "sample" || b.Verdict != "FAIL" || b.Faults != 2 || *b.Seed != 42 || b.StartedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("%+v", b)
	}
	// A missing runs dir is an empty list.
	code, body, _ = get(t, newServeHandler(filepath.Join(runs, "nope"), web), "/api/runs")
	if code != 200 || strings.TrimSpace(body) != "[]" {
		t.Fatalf("%d %s", code, body)
	}
}

func TestServeRunFiles(t *testing.T) {
	runs, web := serveFixture(t)
	must(t, os.WriteFile(filepath.Join(runs, "sample", "42", "notes.txt"), []byte("x"), 0o644))
	h := newServeHandler(runs, web)
	code, body, hdr := get(t, h, "/api/runs/sample/42/trace.jsonl")
	if code != 200 || !strings.Contains(body, `"kind":"run_start"`) || !strings.HasPrefix(hdr.Get("Content-Type"), "text/plain") {
		t.Fatalf("%d %s", code, hdr)
	}
	if code, body, _ := get(t, h, "/api/runs/sample/42/logs/n1.log"); code != 200 || !strings.Contains(body, "n1") {
		t.Fatalf("log %d", code)
	}
	if code, _, hdr := get(t, h, "/api/runs/sample/42/meta.json"); code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "application/json") {
		t.Fatalf("meta %d", code)
	}
	if code, body, _ := get(t, h, "/api/summary/toykv-stale-read"); code != 200 || !strings.Contains(body, `{"seed":"1","verdict":"FAIL"}`) {
		t.Fatalf("summary %d %s", code, body)
	}
	if code, _, _ := get(t, h, "/api/summary/sample"); code != 404 {
		t.Fatalf("missing summary %d", code)
	}
	rejected := []string{
		"/api/runs/sample/42/notes.txt",           // not allow-listed
		"/api/runs/sample/42/minimal.json",        // symlink out of the runs dir
		"/api/runs/sample/42/../../../etc/passwd", // traversal (cleaned by the mux)
		"/api/runs/sample/42/logs/../meta.json",
		"/api/runs/sample/42/%2e%2e/%2e%2e/secret.json",
		"/api/runs/../sample/42/meta.json",
		"/api/runs/sample/..%2f42/meta.json",
		"/api/runs/sample/42//etc/passwd",
		"/api/runs/sample/99/meta.json", // missing run
		"/api/runs/sample/.shrink-123/meta.json",
		"/api/summary/..",
		"/api/nope",
	}
	for _, u := range rejected {
		code, body, _ := get(t, h, u)
		if code == 200 || strings.Contains(body, "secret") || strings.Contains(body, "root:") {
			t.Errorf("%s: %d %q", u, code, body)
		}
	}
}

func TestServeStatic(t *testing.T) {
	runs, web := serveFixture(t)
	h := newServeHandler(runs, web)
	if code, body, _ := get(t, h, "/"); code != 200 || body != "<html>viewer</html>" {
		t.Fatalf("index %d %q", code, body)
	}
	if code, body, _ := get(t, h, "/assets/app.js"); code != 200 || body != "console.log(1)" {
		t.Fatalf("asset %d", code)
	}
	if code, body, _ := get(t, h, "/runs/sample/42"); code != 200 || body != "<html>viewer</html>" {
		t.Fatalf("spa fallback %d", code)
	}
	if code, _, _ := get(t, h, "/assets/missing.js"); code != 404 {
		t.Fatalf("missing asset %d", code)
	}
	code, body, _ := get(t, newServeHandler(runs, filepath.Join(web, "nope")), "/")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "make web") {
		t.Fatalf("missing web dir %d %s", code, body)
	}
}

func TestServeCORS(t *testing.T) {
	runs, web := serveFixture(t)
	h := newServeHandler(runs, web)
	_, _, hdr := get(t, h, "/api/runs", "Origin", "http://localhost:5173")
	if hdr.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatal("no CORS for the dev server")
	}
	_, _, hdr = get(t, h, "/api/runs", "Origin", "http://evil.example")
	if hdr.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS for a foreign origin")
	}
	req := httptest.NewRequest(http.MethodOptions, "/api/runs", nil)
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight %d", rec.Code)
	}
}

func TestResolveInside(t *testing.T) {
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "a"), nil, 0o644))
	if _, ok := resolveInside(root, "a"); !ok {
		t.Fatal("a")
	}
	for _, rel := range []string{"../x", "/etc/passwd", "missing"} {
		if _, ok := resolveInside(root, rel); ok {
			t.Errorf("%s accepted", rel)
		}
	}
}
