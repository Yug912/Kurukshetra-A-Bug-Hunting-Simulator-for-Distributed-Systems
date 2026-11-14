package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"kurukshetra/pkg/core"
)

func init() {
	var addr, webDir string
	var open bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the timeline viewer and an HTTP API over the runs folder",
		Long: "Serve the React timeline viewer (from --web-dir, built with `make web`) and a read-only API:\n" +
			"  GET /api/runs                              runs under --runs-dir, newest first\n" +
			"  GET /api/runs/{scenario}/{run}/{file}      one file of a run (allow-listed)\n" +
			"  GET /api/summary/{scenario}                the explorer's summary.csv as JSON",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd, addr, webDir, open)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:7777", "listen address")
	cmd.Flags().StringVar(&webDir, "web-dir", filepath.Join("web", "timeline", "dist"), "built viewer to serve")
	cmd.Flags().BoolVar(&open, "open", false, "open the viewer in a browser")
	rootCmd.AddCommand(cmd)
}

func runServe(cmd *cobra.Command, addr, webDir string, open bool) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	url := "http://" + ln.Addr().String() + "/"
	srv := &http.Server{Handler: newServeHandler(runsDir(), webDir), ReadHeaderTimeout: 10 * time.Second}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "kuru serve: %s (runs: %s, web: %s)\n", url, runsDir(), webDir)
	if open {
		openBrowser(url)
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	_ = c.Start()
}

// runFiles are the files of a run that may be served (plus logs/<node>.log).
var runFiles = map[string]bool{
	core.TraceFile: true, core.HistoryFile: true, core.ResultFile: true, core.FaultsFile: true,
	core.MetaFile: true, core.ScenarioFile: true, "minimal.json": true, "shrink.log": true,
	"linearizability.html": true,
}

var (
	segmentRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	logRE     = regexp.MustCompile(`^logs/[A-Za-z0-9][A-Za-z0-9_-]*\.log$`)
)

// allowedFile reports whether name (slash-separated, relative to the run dir) may be served.
func allowedFile(name string) bool {
	return runFiles[name] || logRE.MatchString(name)
}

// validSegment reports whether s is a safe single path segment (no dots-only, no separators).
func validSegment(s string) bool {
	return segmentRE.MatchString(s) && !strings.Contains(s, "..")
}

// newServeHandler returns the kuru serve HTTP handler.
func newServeHandler(runsRoot, webDir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		runs, err := listRuns(runsRoot)
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSONResponse(w, runs)
	})
	mux.HandleFunc("GET /api/runs/{scenario}/{run}/{file...}", func(w http.ResponseWriter, r *http.Request) {
		serveRunFile(w, r, runsRoot)
	})
	mux.HandleFunc("GET /api/summary/{scenario}", func(w http.ResponseWriter, r *http.Request) {
		serveSummary(w, r, runsRoot)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		httpError(w, http.StatusNotFound, "not found")
	})
	mux.Handle("/", staticHandler(webDir))
	return withCORS(mux)
}

// withCORS lets the Vite dev server (localhost:5173) call the API.
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Origin") {
		case "http://localhost:5173", "http://127.0.0.1:5173":
			w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeJSONResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// RunEntry is one item of GET /api/runs.
type RunEntry struct {
	ID        string   `json:"id"` // "<scenario>/<run folder>"
	Scenario  string   `json:"scenario"`
	Seed      *int64   `json:"seed"`
	Verdict   string   `json:"verdict"`
	Fidelity  *float64 `json:"fidelity"`
	Faults    int      `json:"faults"`
	StartedAt string   `json:"started_at"`
	ReplayOf  string   `json:"replay_of,omitempty"`
}

// listRuns finds run folders (<root>/<scenario>/<run> containing meta.json or result.json), newest first.
func listRuns(root string) ([]RunEntry, error) {
	out := []RunEntry{}
	scenarios, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, sd := range scenarios {
		if !sd.IsDir() || !validSegment(sd.Name()) {
			continue
		}
		runs, err := os.ReadDir(filepath.Join(root, sd.Name()))
		if err != nil {
			continue
		}
		for _, rd := range runs {
			if !rd.IsDir() || !validSegment(rd.Name()) {
				continue
			}
			if e, ok := readRunEntry(filepath.Join(root, sd.Name(), rd.Name()), sd.Name(), rd.Name()); ok {
				out = append(out, e)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt > out[j].StartedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func readRunEntry(dir, scenarioDir, runDir string) (RunEntry, bool) {
	meta, merr := core.ReadMeta(dir)
	res, rerr := core.ReadResult(dir)
	if merr != nil && rerr != nil {
		return RunEntry{}, false
	}
	e := RunEntry{ID: scenarioDir + "/" + runDir, Scenario: scenarioDir, Verdict: core.VerdictUnknown}
	if merr == nil {
		e.Scenario = meta.Scenario
		seed := meta.Seed
		e.Seed = &seed
		e.ReplayOf = meta.ReplayOf
		if !meta.StartedAt.IsZero() {
			e.StartedAt = meta.StartedAt.UTC().Format(time.RFC3339)
		}
	}
	if rerr == nil {
		e.Verdict = res.Verdict
		e.Fidelity = res.Fidelity
		if e.Seed == nil {
			seed := res.Seed
			e.Seed = &seed
		}
		if e.Scenario == "" {
			e.Scenario = res.Scenario
		}
	}
	if e.Seed == nil {
		if s, err := strconv.ParseInt(runDir, 10, 64); err == nil {
			e.Seed = &s
		}
	}
	if f, err := core.ReadFaults(filepath.Join(dir, core.FaultsFile)); err == nil {
		e.Faults = len(f)
	}
	return e, true
}

// serveRunFile serves one allow-listed file of a run, refusing traversal and symlinks out of the root.
func serveRunFile(w http.ResponseWriter, r *http.Request, root string) {
	scen, run, name := r.PathValue("scenario"), r.PathValue("run"), r.PathValue("file")
	if !validSegment(scen) || !validSegment(run) || path.Clean(name) != name || !allowedFile(name) {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	p, ok := resolveInside(root, filepath.Join(scen, run, filepath.FromSlash(name)))
	if !ok {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	f, err := os.Open(p)
	if err != nil {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	ctype := mime.TypeByExtension(filepath.Ext(name))
	switch filepath.Ext(name) {
	case ".jsonl", ".log", ".yaml":
		ctype = "text/plain; charset=utf-8"
	}
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// resolveInside joins rel onto root and resolves symlinks; ok is false if the
// result escapes root (after resolving root's own symlinks) or does not exist.
func resolveInside(root, rel string) (string, bool) {
	if filepath.IsAbs(rel) {
		return "", false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	realRoot, err = filepath.Abs(realRoot)
	if err != nil {
		return "", false
	}
	p, err := filepath.EvalSymlinks(filepath.Join(root, rel))
	if err != nil {
		return "", false
	}
	p, err = filepath.Abs(p)
	if err != nil {
		return "", false
	}
	r, err := filepath.Rel(realRoot, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", false
	}
	return p, true
}

// serveSummary returns <root>/<scenario>/summary.csv as a JSON array of objects keyed by the header row.
func serveSummary(w http.ResponseWriter, r *http.Request, root string) {
	scen := r.PathValue("scenario")
	if !validSegment(scen) {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	p, ok := resolveInside(root, filepath.Join(scen, "summary.csv"))
	if !ok {
		httpError(w, http.StatusNotFound, "no summary.csv for "+scen)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		httpError(w, http.StatusNotFound, "no summary.csv for "+scen)
		return
	}
	defer func() { _ = f.Close() }()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "summary.csv: "+err.Error())
		return
	}
	out := []map[string]string{}
	if len(rows) > 0 {
		header := rows[0]
		for _, row := range rows[1:] {
			m := map[string]string{}
			for i, h := range header {
				if i < len(row) {
					m[h] = row[i]
				}
			}
			out = append(out, m)
		}
	}
	writeJSONResponse(w, out)
}

const missingWebPage = `<!doctype html><html><head><meta charset="utf-8"><title>kuru serve</title></head>
<body style="font-family:system-ui;max-width:640px;margin:40px auto;line-height:1.5">
<h1>Viewer not built</h1>
<p>The timeline viewer was not found in <code>%s</code>. Build it with:</p>
<pre>make web</pre>
<p>then restart <code>kuru serve</code>. The API is available meanwhile at <a href="/api/runs">/api/runs</a>.</p>
</body></html>`

// staticHandler serves the built viewer with an SPA fallback to index.html.
func staticHandler(dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := filepath.Join(dir, "index.html")
		if _, err := os.Stat(index); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, missingWebPage, dir)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		if clean != "/" {
			if p, ok := resolveInside(dir, filepath.FromSlash(strings.TrimPrefix(clean, "/"))); ok {
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					http.ServeFile(w, r, p)
					return
				}
			}
			if path.Ext(clean) != "" {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, index)
	})
}
