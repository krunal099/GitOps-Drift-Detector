// Package server serves a live web dashboard showing the latest drift report.
package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"

	"github.com/krunalp/gitops-drift-detector/pkg/report"
)

// Server holds the HTTP server and a function to get the latest report.
type Server struct {
	port      int
	getReport func() *report.DriftReport
	tmpl      *template.Template
}

// New creates a Server. getReport is called on every page load to get fresh data.
func New(port int, getReport func() *report.DriftReport) *Server {
	funcs := template.FuncMap{
		// toJSON converts any value (interface{}) to a readable JSON string for display
		"toJSON": func(v interface{}) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
	}
	return &Server{
		port:      port,
		getReport: getReport,
		tmpl:      template.Must(template.New("dashboard").Funcs(funcs).Parse(dashboardHTML)),
	}
}

// Start launches the HTTP server in the background and prints the URL.
func (s *Server) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/api/report", s.handleJSON)

	addr := fmt.Sprintf(":%d", s.port)
	fmt.Printf("\n  Dashboard → http://localhost%s\n\n", addr)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			fmt.Printf("dashboard server error: %v\n", err)
		}
	}()
}

// handleDashboard renders the HTML page with the latest report data injected.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.tmpl.Execute(w, s.getReport())
}

// handleJSON returns the raw drift report as JSON — useful for scripting or CI.
func (s *Server) handleJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rep := s.getReport()
	if rep == nil {
		w.Write([]byte(`{"status":"waiting for first check"}`))
		return
	}
	json.NewEncoder(w).Encode(rep)
}

// dashboardHTML is the entire dashboard — one self-contained HTML page.
// It auto-refreshes every 30 seconds via the <meta> tag.
// No external CSS or JS libraries — works offline.
var dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta http-equiv="refresh" content="30">
  <title>Drift Detector</title>
  <style>
    *{box-sizing:border-box;margin:0;padding:0}
    body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;background:#f1f5f9;color:#1e293b}

    /* ── header ── */
    header{background:#0f172a;color:#fff;padding:18px 32px;display:flex;align-items:center;gap:12px}
    header h1{font-size:18px;font-weight:700;letter-spacing:-.3px}
    .tag{font-size:11px;padding:3px 10px;border-radius:20px;background:#1e3a5f;color:#93c5fd;font-weight:600}
    .tag.dryrun{background:#78350f;color:#fde68a}
    .tag.refresh{margin-left:auto;background:#1e293b;color:#94a3b8}

    /* ── layout ── */
    main{padding:32px;max-width:1080px;margin:0 auto}

    /* ── waiting state ── */
    .waiting{text-align:center;padding:80px 32px;color:#94a3b8}
    .waiting h2{font-size:22px;margin-bottom:8px;color:#64748b}

    /* ── summary cards ── */
    .summary{display:grid;grid-template-columns:repeat(5,1fr);gap:14px;margin-bottom:32px}
    .stat{background:#fff;border-radius:12px;padding:20px;text-align:center;box-shadow:0 1px 3px rgba(0,0,0,.07)}
    .stat .num{font-size:38px;font-weight:800;line-height:1}
    .stat .lbl{font-size:11px;color:#94a3b8;margin-top:6px;text-transform:uppercase;letter-spacing:.6px;font-weight:600}
    .n-total{color:#3b82f6}
    .n-drifted{color:#ef4444}
    .n-missing{color:#f97316}
    .n-clean{color:#22c55e}
    .n-ignored{color:#94a3b8}

    /* ── section heading ── */
    .section-title{font-size:12px;font-weight:700;color:#64748b;text-transform:uppercase;letter-spacing:.8px;margin-bottom:14px}

    /* ── resource card ── */
    .card{background:#fff;border-radius:12px;padding:18px 22px;margin-bottom:10px;
          box-shadow:0 1px 3px rgba(0,0,0,.07);border-left:4px solid #e2e8f0}
    .card.drifted{border-left-color:#ef4444}
    .card.clean{border-left-color:#22c55e}
    .card.missing{border-left-color:#f97316}
    .card.ignored{border-left-color:#cbd5e1}

    .card-top{display:flex;align-items:center;gap:10px}
    .kind{font-size:11px;color:#94a3b8;font-weight:600;text-transform:uppercase;letter-spacing:.4px}
    .name{font-weight:700;font-size:15px}
    .ns{font-size:12px;color:#94a3b8}

    .pill{font-size:10px;font-weight:700;padding:3px 10px;border-radius:20px;margin-left:auto;text-transform:uppercase;letter-spacing:.5px}
    .pill.drifted{background:#fee2e2;color:#dc2626}
    .pill.clean{background:#dcfce7;color:#16a34a}
    .pill.missing{background:#ffedd5;color:#ea580c}
    .pill.ignored{background:#f8fafc;color:#94a3b8}

    /* ── field diff ── */
    .fields{margin-top:14px;padding-top:14px;border-top:1px solid #f1f5f9}
    .field{margin-bottom:12px}
    .field-path{font-family:'SF Mono','Fira Code',monospace;font-size:12px;font-weight:700;
                color:#475569;margin-bottom:6px;padding:4px 8px;background:#f8fafc;
                border-radius:4px;display:inline-block}
    .diff-row{display:grid;grid-template-columns:1fr 1fr;gap:8px;margin-top:4px}
    .diff-val{padding:8px 12px;border-radius:8px;font-family:monospace;font-size:13px}
    .diff-val .lbl{font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:.5px;margin-bottom:3px;opacity:.65}
    .diff-desired{background:#f0fdf4;color:#15803d;border:1px solid #bbf7d0}
    .diff-actual{background:#fef2f2;color:#b91c1c;border:1px solid #fecaca}

    footer{text-align:center;padding:24px;color:#94a3b8;font-size:12px}
    footer a{color:#64748b}
  </style>
</head>
<body>

<header>
  <h1>GitOps Drift Detector</h1>
  {{if .}}
    {{if .DryRun}}<span class="tag dryrun">DRY RUN — read only</span>{{end}}
  {{end}}
  <span class="tag refresh">Auto-refreshes every 30s</span>
</header>

<main>
  {{if not .}}

  <div class="waiting">
    <h2>Waiting for first check...</h2>
    <p>The detector is running. This page will update automatically.</p>
  </div>

  {{else}}

  <!-- summary row -->
  <div class="summary">
    <div class="stat"><div class="num n-total">{{.TotalResources}}</div><div class="lbl">Total</div></div>
    <div class="stat"><div class="num n-drifted">{{.DriftedCount}}</div><div class="lbl">Drifted</div></div>
    <div class="stat"><div class="num n-missing">{{.MissingCount}}</div><div class="lbl">Missing</div></div>
    <div class="stat"><div class="num n-clean">{{.CleanCount}}</div><div class="lbl">Clean</div></div>
    <div class="stat"><div class="num n-ignored">{{.IgnoredCount}}</div><div class="lbl">Ignored</div></div>
  </div>

  <!-- resource list -->
  <div class="section-title">
    Resources &nbsp;·&nbsp; Last checked {{.GeneratedAt.Format "2006-01-02 15:04:05 UTC"}}
  </div>

  {{range .Resources}}
  <div class="card {{.Status}}">
    <div class="card-top">
      <span class="kind">{{.Kind}}</span>
      <span class="name">{{.Name}}</span>
      <span class="ns">{{.Namespace}}</span>
      <span class="pill {{.Status}}">{{.Status}}</span>
    </div>

    {{if .Fields}}
    <div class="fields">
      {{range .Fields}}
      <div class="field">
        <span class="field-path">~ {{.Path}}</span>
        <div class="diff-row">
          <div class="diff-val diff-desired">
            <div class="lbl">Git says (desired)</div>
            {{toJSON .Desired}}
          </div>
          <div class="diff-val diff-actual">
            <div class="lbl">Cluster has (actual)</div>
            {{toJSON .Actual}}
          </div>
        </div>
      </div>
      {{end}}
    </div>
    {{end}}

  </div>
  {{end}}

  {{end}}
</main>

<footer>
  GitOps Drift Detector &nbsp;·&nbsp; <a href="/api/report">JSON report</a>
</footer>

</body>
</html>`
