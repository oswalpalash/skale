package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	htmltemplate "html/template"
	"sort"
	"strings"
	"time"
)

// RenderHTML renders the read-only workload qualification console.
func RenderHTML(overview Overview) ([]byte, error) {
	payload, err := json.Marshal(overview)
	if err != nil {
		return nil, err
	}

	view := htmlView{
		Overview:         overview,
		GeneratedAtLabel: formatTime(overview.GeneratedAt),
		InventoryAtLabel: formatTime(overview.InventoryAt),
		OverviewJSON:     htmltemplate.JS(payload),
		Namespaces:       namespaceViews(overview.Workloads),
	}
	view.NamespaceGroups = namespaceGroups(view.Namespaces)

	var buffer bytes.Buffer
	if err := dashboardTemplate.Execute(&buffer, view); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

type htmlView struct {
	Overview         Overview
	GeneratedAtLabel string
	InventoryAtLabel string
	OverviewJSON     htmltemplate.JS
	Namespaces       []namespaceView
	NamespaceGroups  []namespaceGroup
}

type namespaceView struct {
	Name                  string
	Total                 int
	Good                  int
	NeedsConfiguration    int
	NeedsScalingContract  int
	Unsupported           int
	RecommendationSummary string
	Tier                  string
}

type namespaceGroup struct {
	Label      string
	ClassToken string
	Namespaces []namespaceView
}

func namespaceViews(workloads []Workload) []namespaceView {
	byNamespace := map[string]*namespaceView{}
	for _, workload := range workloads {
		name := workload.Namespace
		if name == "" {
			name = "default"
		}
		view := byNamespace[name]
		if view == nil {
			view = &namespaceView{Name: name}
			byNamespace[name] = view
		}
		view.Total++
		switch workload.Qualification {
		case QualificationPolicyBacked, "candidate":
			view.Good++
		case "needs configuration":
			view.NeedsConfiguration++
		case "needs scaling contract":
			view.NeedsScalingContract++
		default:
			view.Unsupported++
		}
		if workload.RecommendationState != "" {
			view.RecommendationSummary = workload.RecommendationState
		}
	}

	out := make([]namespaceView, 0, len(byNamespace))
	for _, view := range byNamespace {
		switch {
		case view.Good > 0:
			view.Tier = "good fit"
		case view.NeedsConfiguration > 0:
			view.Tier = "needs config"
		case view.NeedsScalingContract > 0:
			view.Tier = "needs contract"
		default:
			view.Tier = "unsupported"
		}
		if view.RecommendationSummary == "" {
			view.RecommendationSummary = "no live recommendation"
		}
		out = append(out, *view)
	}
	sort.Slice(out, func(i, j int) bool {
		if namespaceRank(out[i]) != namespaceRank(out[j]) {
			return namespaceRank(out[i]) < namespaceRank(out[j])
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func namespaceGroups(namespaces []namespaceView) []namespaceGroup {
	definitions := []struct {
		tier  string
		label string
	}{
		{tier: "good fit", label: "Good fit"},
		{tier: "needs config", label: "Needs configuration"},
		{tier: "needs contract", label: "Needs scaling contract"},
		{tier: "unsupported", label: "Outside v1 wedge"},
	}
	groups := make([]namespaceGroup, 0, len(definitions))
	for _, definition := range definitions {
		group := namespaceGroup{
			Label:      definition.label,
			ClassToken: statusTone(definition.tier),
		}
		for _, namespace := range namespaces {
			if namespace.Tier == definition.tier {
				group.Namespaces = append(group.Namespaces, namespace)
			}
		}
		if len(group.Namespaces) > 0 {
			groups = append(groups, group)
		}
	}
	return groups
}

func namespaceRank(view namespaceView) int {
	switch view.Tier {
	case "good fit":
		return 0
	case "needs config":
		return 1
	case "needs contract":
		return 2
	default:
		return 3
	}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format(time.RFC3339)
}

func escapeJSString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

func contractLabel(value string) string {
	switch value {
	case ScalingContractHPA:
		return "HPA"
	case ScalingContractExplicitPolicy:
		return "Explicit policy"
	case ScalingContractMissing:
		return "Missing"
	default:
		return "Unsupported"
	}
}

func statusTone(value string) string {
	token := strings.ToLower(strings.TrimSpace(value))
	token = strings.ReplaceAll(token, " ", "-")
	token = strings.ReplaceAll(token, "_", "-")
	if token == "" {
		return "unknown"
	}
	return token
}

func replicasLabel(workload Workload) string {
	if workload.CurrentReplicas == nil && workload.RecommendedReplicas == nil {
		return "not available"
	}
	current := "?"
	if workload.CurrentReplicas != nil {
		current = fmt.Sprintf("%d", *workload.CurrentReplicas)
	}
	if workload.RecommendationState == "" {
		return current
	}
	recommended := "?"
	if workload.RecommendedReplicas != nil {
		recommended = fmt.Sprintf("%d", *workload.RecommendedReplicas)
	}
	return current + " -> " + recommended
}

func reasonsLabel(workload Workload) string {
	if len(workload.SuppressionReasons) > 0 {
		return strings.Join(workload.SuppressionReasons, ", ")
	}
	if len(workload.MissingPrerequisites) > 0 {
		return "missing " + strings.Join(workload.MissingPrerequisites, ", ")
	}
	codes := make([]string, 0, len(workload.Reasons))
	for _, reason := range workload.Reasons {
		if reason.Code != "" {
			codes = append(codes, reason.Code)
		}
	}
	if len(codes) > 0 {
		return strings.Join(codes, ", ")
	}
	return "none"
}

var dashboardTemplate = htmltemplate.Must(htmltemplate.New("dashboard").Funcs(htmltemplate.FuncMap{
	"escapeJSString": escapeJSString,
	"classToken":     statusTone,
}).Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Skale Workload Qualification</title>
  <style>
    :root {
      --bg: #0f1412;
      --panel: #f5f7f6;
      --panel-2: #e6ece9;
      --ink: #17201d;
      --muted: #6c776f;
      --line: #c9d2cd;
      --green: #0e8068;
      --teal: #126c7a;
      --amber: #a86f15;
      --red: #aa4337;
      --violet: #665191;
      --shadow: rgba(3, 8, 6, 0.22);
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      background: var(--bg);
      color: var(--ink);
      font-family: Avenir Next, Avenir, Verdana, sans-serif;
      letter-spacing: 0;
    }
    button { font: inherit; }
    .shell {
      min-height: 100vh;
      display: grid;
      grid-template-rows: auto 1fr;
    }
    .mast {
      padding: 22px 28px;
      color: #f5f7f6;
      border-bottom: 1px solid rgba(245, 247, 246, 0.16);
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 28px;
      align-items: end;
    }
    .mast h1 {
      margin: 0;
      font-size: clamp(30px, 5vw, 58px);
      line-height: 0.95;
      font-weight: 900;
      max-width: 980px;
    }
    .mast p {
      margin: 10px 0 0;
      max-width: 850px;
      color: #bcc9c1;
      font-size: 14px;
    }
    .clock {
      text-align: right;
      color: #bcc9c1;
      font-size: 12px;
      white-space: nowrap;
    }
    .workspace {
      display: grid;
      grid-template-columns: 360px minmax(0, 1fr);
      min-height: 0;
    }
    .rail {
      background: #18211e;
      color: #f5f7f6;
      border-right: 1px solid rgba(245, 247, 246, 0.14);
      padding: 22px;
      overflow: auto;
    }
    .rail-title {
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 16px;
      margin-bottom: 16px;
    }
    .rail-title h2 {
      margin: 0;
      font-size: 15px;
      text-transform: uppercase;
      letter-spacing: 0.08em;
    }
    .count-pill {
      border: 1px solid rgba(245, 247, 246, 0.24);
      color: #dbe5df;
      padding: 5px 9px;
      font-size: 12px;
    }
    .namespace-list {
      display: grid;
      gap: 16px;
    }
    .namespace-group {
      display: grid;
      gap: 9px;
    }
    .namespace-group-title {
      color: #aebbb4;
      font-size: 11px;
      text-transform: uppercase;
      letter-spacing: 0.08em;
      display: flex;
      align-items: center;
      gap: 8px;
    }
    .namespace-group-title::before {
      content: "";
      width: 8px;
      height: 8px;
      background: #7b857f;
    }
    .namespace-group-title.good-fit::before { background: #35c5a7; }
    .namespace-group-title.needs-config::before { background: #e3a13b; }
    .namespace-group-title.needs-contract::before { background: #ba9b70; }
    .namespace-group-title.unsupported::before { background: #d76355; }
    .namespace-group-list {
      display: grid;
      gap: 10px;
    }
    .namespace-card {
      width: 100%;
      text-align: left;
      border: 1px solid rgba(245, 247, 246, 0.16);
      background: #202c28;
      color: #f5f7f6;
      padding: 14px;
      cursor: pointer;
      transition: transform 140ms ease, border-color 140ms ease, background 140ms ease;
    }
    .namespace-card:hover,
    .namespace-card.active {
      transform: translateX(3px);
      border-color: #89d8ca;
      background: #263631;
    }
    .ns-top {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      align-items: start;
    }
    .ns-name {
      font-weight: 900;
      font-size: 17px;
      overflow-wrap: anywhere;
    }
    .ns-tier {
      white-space: nowrap;
      font-size: 11px;
      padding: 4px 7px;
      border-left: 4px solid #86928b;
      background: rgba(255,255,255,0.06);
    }
    .ns-tier.good-fit { border-left-color: #35c5a7; }
    .ns-tier.needs-config { border-left-color: #e3a13b; }
    .ns-tier.needs-contract { border-left-color: #ba9b70; }
    .ns-tier.unsupported { border-left-color: #d76355; }
    .ns-stats {
      display: grid;
      grid-template-columns: repeat(4, 1fr);
      gap: 6px;
      margin-top: 14px;
    }
    .ns-stat {
      border-top: 1px solid rgba(245, 247, 246, 0.18);
      padding-top: 7px;
    }
    .ns-stat b {
      display: block;
      font-size: 18px;
    }
    .ns-stat span {
      color: #aebbb4;
      font-size: 10px;
      text-transform: uppercase;
    }
    .stage {
      background: var(--panel);
      min-width: 0;
      overflow: auto;
    }
    .stage-inner {
      padding: 28px;
      display: grid;
      gap: 22px;
    }
    .hero-panel {
      border: 1px solid var(--line);
      background: var(--panel);
      box-shadow: 0 18px 50px var(--shadow);
      padding: 22px;
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 22px;
      align-items: end;
    }
    .eyebrow {
      color: var(--muted);
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: 0.08em;
      margin-bottom: 6px;
    }
    .hero-panel h2 {
      margin: 0;
      font-size: clamp(24px, 4vw, 42px);
      line-height: 1;
    }
    .hero-panel p {
      margin: 10px 0 0;
      color: var(--muted);
      max-width: 820px;
      font-size: 14px;
    }
    .score {
      min-width: 220px;
      display: grid;
      grid-template-columns: repeat(2, 1fr);
      border: 1px solid var(--line);
    }
    .score div {
      padding: 12px;
      border-right: 1px solid var(--line);
      border-bottom: 1px solid var(--line);
    }
    .score div:nth-child(2n) { border-right: 0; }
    .score div:nth-last-child(-n+2) { border-bottom: 0; }
    .score b {
      display: block;
      font-size: 24px;
    }
    .score span {
      color: var(--muted);
      font-size: 11px;
      text-transform: uppercase;
    }
    .layout {
      display: grid;
      grid-template-columns: minmax(340px, 0.75fr) minmax(0, 1.25fr);
      gap: 18px;
      align-items: start;
    }
    .panel {
      border: 1px solid var(--line);
      background: #ffffff;
      box-shadow: 0 10px 26px rgba(21, 28, 24, 0.08);
    }
    .panel-head {
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 12px;
      padding: 14px 16px;
      border-bottom: 1px solid var(--line);
      background: var(--panel-2);
    }
    .panel-head h3 {
      margin: 0;
      font-size: 13px;
      text-transform: uppercase;
      letter-spacing: 0.07em;
    }
    .workload-list {
      display: grid;
    }
    .workload-row {
      width: 100%;
      text-align: left;
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 12px;
      border: 0;
      border-bottom: 1px solid var(--line);
      background: transparent;
      padding: 14px 16px;
      cursor: pointer;
    }
    .workload-row:hover,
    .workload-row.active {
      background: #edf3f0;
    }
    .workload-row:last-child { border-bottom: 0; }
    .w-name {
      font-weight: 900;
      overflow-wrap: anywhere;
    }
    .w-meta {
      margin-top: 4px;
      color: var(--muted);
      font-size: 12px;
    }
    .badge {
      display: inline-flex;
      align-items: center;
      min-height: 25px;
      padding: 4px 8px;
      border-left: 5px solid #7b857f;
      background: #e7eeea;
      font-size: 12px;
      font-weight: 800;
      white-space: nowrap;
    }
    .badge.policy-backed { border-left-color: var(--teal); }
    .badge.candidate { border-left-color: var(--green); }
    .badge.needs-configuration { border-left-color: var(--amber); }
    .badge.needs-scaling-contract { border-left-color: #7f6743; }
    .badge.low-confidence { border-left-color: var(--violet); }
    .badge.unsupported { border-left-color: var(--red); }
    .detail {
      min-height: 560px;
    }
    .detail-body {
      padding: 18px;
      display: grid;
      gap: 18px;
    }
    .detail-title {
      display: flex;
      justify-content: space-between;
      align-items: start;
      gap: 18px;
    }
    .detail-title h3 {
      margin: 0;
      font-size: 28px;
      line-height: 1.05;
      overflow-wrap: anywhere;
    }
    .kv-grid {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      border: 1px solid var(--line);
    }
    .kv {
      padding: 12px;
      border-right: 1px solid var(--line);
      min-height: 78px;
    }
    .kv:last-child { border-right: 0; }
    .kv span {
      display: block;
      color: var(--muted);
      font-size: 11px;
      text-transform: uppercase;
      margin-bottom: 7px;
    }
    .kv b {
      font-size: 15px;
      overflow-wrap: anywhere;
    }
    .evidence-card {
      border: 1px solid var(--line);
      background: #ffffff;
      padding: 16px;
    }
    .section-title {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      margin-bottom: 10px;
      color: var(--muted);
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: 0.07em;
    }
    .replica-chart {
      width: 100%;
      height: 210px;
      display: block;
      margin: 2px 0 12px;
    }
    .replica-chart .axis { stroke: #cdd7d2; stroke-width: 1; }
    .replica-chart .grid { stroke: #edf1ee; stroke-width: 1; }
    .replica-chart .current-line { stroke: var(--teal); stroke-width: 4; fill: none; stroke-linecap: round; stroke-linejoin: round; }
    .replica-chart .recommended-line { stroke: var(--amber); stroke-width: 4; fill: none; stroke-linecap: round; stroke-linejoin: round; }
    .replica-chart .demand-line { stroke: #2b4650; stroke-width: 2; fill: none; stroke-linecap: round; stroke-linejoin: round; opacity: 0.72; }
    .replica-chart .pressure-area { fill: rgba(170, 67, 55, 0.16); stroke: none; }
    .replica-chart .point-current { fill: var(--teal); }
    .replica-chart .point-recommended { fill: var(--amber); }
    .replica-chart text {
      fill: #52615a;
      font-size: 12px;
      font-weight: 700;
    }
    .graph-legend {
      display: flex;
      flex-wrap: wrap;
      gap: 8px 14px;
      align-items: center;
      margin: -2px 0 8px;
      color: var(--muted);
      font-size: 11px;
      line-height: 1.25;
    }
    .graph-legend span {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      min-height: 18px;
    }
    .legend-swatch {
      width: 18px;
      height: 4px;
      display: inline-block;
      background: #7b857f;
    }
    .legend-swatch.replicas { background: var(--teal); }
    .legend-swatch.demand { background: #2b4650; height: 2px; }
    .legend-swatch.pressure {
      height: 12px;
      background: rgba(170, 67, 55, 0.22);
      border: 1px solid rgba(170, 67, 55, 0.36);
    }
    .timeline-bar {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      align-items: center;
      margin-bottom: 10px;
    }
    .timeline-window {
      display: inline-grid;
      grid-auto-flow: column;
      border: 1px solid var(--line);
      background: #f8faf9;
    }
    .timeline-window button {
      border: 0;
      border-right: 1px solid var(--line);
      background: transparent;
      color: var(--muted);
      min-width: 48px;
      min-height: 30px;
      padding: 5px 9px;
      font-size: 12px;
      font-weight: 800;
      cursor: pointer;
    }
    .timeline-window button:last-child { border-right: 0; }
    .timeline-window button:hover,
    .timeline-window button.active {
      background: #18211e;
      color: #f5f7f6;
    }
    .evidence-strip {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 10px;
      margin: 8px 0 12px;
    }
    .evidence-metric {
      border: 1px solid var(--line);
      background: #f8faf9;
      padding: 10px;
      min-height: 74px;
    }
    .evidence-metric span {
      display: block;
      color: var(--muted);
      font-size: 10px;
      text-transform: uppercase;
      margin-bottom: 6px;
    }
    .evidence-metric b {
      display: block;
      font-size: 18px;
      line-height: 1.05;
    }
    .evidence-metric small {
      display: block;
      color: var(--muted);
      margin-top: 5px;
      font-size: 11px;
      line-height: 1.25;
    }
    .notice {
      padding: 12px 14px;
      border-left: 5px solid var(--amber);
      background: #f3ead7;
      color: #4e4738;
      font-size: 13px;
      line-height: 1.45;
    }
    .reason-list {
      display: grid;
      gap: 8px;
    }
    .reason-item {
      padding: 10px 12px;
      background: #edf1ee;
      border-left: 4px solid #8c795a;
      font-size: 13px;
    }
    .empty-state {
      border: 1px dashed var(--line);
      padding: 44px;
      color: var(--muted);
      background: #ffffff;
    }
    .hidden { display: none; }
    @media (max-width: 1100px) {
      .workspace { grid-template-columns: 1fr; }
      .rail { max-height: none; }
      .layout { grid-template-columns: 1fr; }
      .hero-panel { grid-template-columns: 1fr; }
      .score { width: 100%; }
    }
    @media (max-width: 760px) {
      .mast { grid-template-columns: 1fr; padding: 20px; }
      .clock { text-align: left; white-space: normal; }
      .stage-inner, .rail { padding: 16px; }
      .kv-grid { grid-template-columns: 1fr 1fr; }
      .score { grid-template-columns: 1fr 1fr; }
      .evidence-strip { grid-template-columns: 1fr; }
      .timeline-bar { align-items: flex-start; flex-direction: column; }
      .timeline-window { width: 100%; grid-template-columns: repeat(4, 1fr); grid-auto-flow: row; }
    }
  </style>
</head>
<body>
  <main class="shell">
    <section class="mast">
      <div>
        <h1>Workload Qualification Console</h1>
        <p>Namespaces first. Drill into workloads. Replica recommendations appear only where Skale has explicit scaling contract and policy-backed evidence.</p>
      </div>
      <div class="clock">
        <div>generated {{.GeneratedAtLabel}}</div>
        <div>inventory {{.InventoryAtLabel}}</div>
      </div>
    </section>

    <section class="workspace">
      <aside class="rail">
        <div class="rail-title">
          <h2>Namespaces</h2>
          <span class="count-pill">{{len .Namespaces}}</span>
        </div>
        <div class="namespace-list" id="namespace-list">
          {{range .NamespaceGroups}}
          <section class="namespace-group">
            <div class="namespace-group-title {{.ClassToken}}">{{.Label}}</div>
            <div class="namespace-group-list">
              {{range .Namespaces}}
              <button class="namespace-card" data-namespace="{{.Name}}">
                <div class="ns-top">
                  <div class="ns-name">{{.Name}}</div>
                  <div class="ns-tier {{classToken .Tier}}">{{.Tier}}</div>
                </div>
                <div class="ns-stats">
                  <div class="ns-stat"><b>{{.Good}}</b><span>good</span></div>
                  <div class="ns-stat"><b>{{.NeedsConfiguration}}</b><span>config</span></div>
                  <div class="ns-stat"><b>{{.NeedsScalingContract}}</b><span>contract</span></div>
                  <div class="ns-stat"><b>{{.Unsupported}}</b><span>blocked</span></div>
                </div>
              </button>
              {{end}}
            </div>
          </section>
          {{end}}
        </div>
      </aside>

      <section class="stage">
        <div class="stage-inner">
          <section class="hero-panel">
            <div>
              <div class="eyebrow">Cluster evidence</div>
              <h2 id="hero-title">Pick namespace</h2>
              <p id="hero-copy">Start with namespaces that have policy-backed or candidate workloads. Workloads without scaling contracts stay visible, but Skale withholds replica advice.</p>
            </div>
            <div class="score" aria-label="cluster summary">
              <div><b id="score-policy">{{.Overview.Summary.PolicyBacked}}</b><span>policy-backed</span></div>
              <div><b id="score-candidate">{{.Overview.Summary.Candidates}}</b><span>candidate</span></div>
              <div><b id="score-config">{{.Overview.Summary.NeedsConfiguration}}</b><span>needs config</span></div>
              <div><b id="score-contract">{{.Overview.Summary.NeedsScalingContract}}</b><span>needs contract</span></div>
            </div>
          </section>

          <section class="layout">
            <section class="panel">
              <div class="panel-head">
                <h3 id="workload-panel-title">Workloads</h3>
                <span class="count-pill" id="workload-count">0</span>
              </div>
              <div class="workload-list" id="workload-list">
                <div class="empty-state">Select namespace to inspect workloads.</div>
              </div>
            </section>

            <section class="panel detail">
              <div class="panel-head">
                <h3>Workload detail</h3>
                <span class="count-pill" id="detail-state">none</span>
              </div>
              <div class="detail-body" id="detail-body">
                <div class="empty-state">Select workload to inspect scaling contract, telemetry, recommendation state, and decision evidence.</div>
              </div>
            </section>
          </section>
        </div>
      </section>
    </section>
  </main>

  <script id="overview-data" type="application/json">{{.OverviewJSON}}</script>
  <script>
    let overview = JSON.parse(document.getElementById('overview-data').textContent);
    const nsList = document.getElementById('namespace-list');
    const workloadList = document.getElementById('workload-list');
    const workloadCount = document.getElementById('workload-count');
    const workloadTitle = document.getElementById('workload-panel-title');
    const detailBody = document.getElementById('detail-body');
    const detailState = document.getElementById('detail-state');
    const heroTitle = document.getElementById('hero-title');
    const heroCopy = document.getElementById('hero-copy');
    const scorePolicy = document.getElementById('score-policy');
    const scoreCandidate = document.getElementById('score-candidate');
    const scoreConfig = document.getElementById('score-config');
    const scoreContract = document.getElementById('score-contract');

    let byNamespace = buildNamespaceIndex(overview.workloads);
    let selectedNamespace = '';
    let selectedWorkloadId = '';
    let selectedLookback = '30m';
    const lookbackOptions = ['30m', '1h', '3h', '6h'];
    const timelines = {};

    function buildNamespaceIndex(workloads) {
      return workloads.reduce((acc, workload) => {
        const namespace = workload.namespace || 'default';
        if (!acc[namespace]) acc[namespace] = [];
        acc[namespace].push(workload);
        return acc;
      }, {});
    }

    function tone(value) {
      return String(value || 'unknown').toLowerCase().replaceAll(' ', '-').replaceAll('_', '-');
    }

    function contractLabel(value) {
      if (value === 'hpa') return 'HPA';
      if (value === 'explicitPolicy') return 'Explicit policy';
      if (value === 'missing') return 'Missing';
      return 'Unsupported';
    }

    function telemetryLabel(workload) {
      const value = workload.telemetryState;
      const message = workload.telemetryMessage || '';
      if (value === 'unsupported' || value === 'unavailable') {
        if (message.includes('not sufficient') || message.includes('missing ') || message.includes('maximum telemetry gap')) {
          return 'learning telemetry';
        }
        return 'Prometheus queries missing';
      }
      if (value === 'degraded') return 'degraded';
      if (value === 'ready') return 'ready';
      return value || 'not evaluated';
    }

    function workloadSort(a, b) {
      const rank = {
        'policy-backed': 0,
        'candidate': 1,
        'needs configuration': 2,
        'needs scaling contract': 3,
        'low confidence': 4,
        'unsupported': 5
      };
      return (rank[a.qualification] ?? 9) - (rank[b.qualification] ?? 9) || a.name.localeCompare(b.name);
    }

	    function replicasLabel(workload) {
	      const current = workload.currentReplicas == null ? 'unknown' : String(workload.currentReplicas);
	      if (workload.recommendedReplicas == null || workload.recommendationState === 'unavailable') return current;
	      return current + ' -> ' + String(workload.recommendedReplicas);
	    }

	    function recommendationLabel(workload) {
	      if (workload.recommendedReplicas == null || workload.recommendationState === 'unavailable') return 'none';
	      return String(workload.recommendedReplicas);
	    }

	    function reasons(workload) {
	      if (workload.suppressionReasons && workload.suppressionReasons.length) return workload.suppressionReasons;
	      if (workload.missingPrerequisites && workload.missingPrerequisites.length) return workload.missingPrerequisites.map(item => 'missing ' + item);
	      if (workload.reasons && workload.reasons.length) return workload.reasons.map(reason => reason.code || reason.message).filter(Boolean);
	      return ['none'];
	    }

    function selectNamespace(namespace, preferredWorkloadId) {
      selectedNamespace = namespace;
      [...nsList.querySelectorAll('.namespace-card')].forEach(card => {
        card.classList.toggle('active', card.dataset.namespace === namespace);
	      });
	      const workloads = [...(byNamespace[namespace] || [])].sort(workloadSort);
	      workloadTitle.textContent = namespace + ' workloads';
	      workloadCount.textContent = workloads.length;
	      const good = workloads.filter(w => w.qualification === 'policy-backed' || w.qualification === 'candidate').length;
	      const setup = workloads.filter(w => w.qualification === 'needs configuration' || w.qualification === 'needs scaling contract').length;
	      const policyBacked = workloads.filter(w => w.qualification === 'policy-backed').length;
	      const candidate = workloads.filter(w => w.qualification === 'candidate').length;
	      const needsConfig = workloads.filter(w => w.qualification === 'needs configuration').length;
	      const needsContract = workloads.filter(w => w.qualification === 'needs scaling contract').length;
	      heroTitle.textContent = namespace;
	      heroCopy.textContent = good + ' good-fit workload(s), ' + setup + ' setup-needed workload(s), ' + (workloads.length - good - setup) + ' unsupported workload(s).';
	      scorePolicy.textContent = policyBacked;
	      scoreCandidate.textContent = candidate;
	      scoreConfig.textContent = needsConfig;
	      scoreContract.textContent = needsContract;
	      workloadList.innerHTML = workloads.map(w => workloadRowHTML(w)).join('');
      workloadList.querySelectorAll('.workload-row').forEach(button => {
        button.addEventListener('click', () => selectWorkload(button.dataset.workloadId, true));
      });
	      if (workloads.length > 0) {
	        const selected = workloads.find(workload => workload.id === preferredWorkloadId) || workloads[0];
	        selectWorkload(selected.id, true);
	      }
    }

	    function workloadRowHTML(workload) {
	      return '<button class="workload-row" data-workload-id="' + escapeHTML(workload.id) + '">' +
	        '<div>' +
	          '<div class="w-name">' + escapeHTML(workload.name) + '</div>' +
	          '<div class="w-meta">' + escapeHTML(workload.apiVersion || '') + ' ' + escapeHTML(workload.kind || '') + (workload.hpaName ? ' · HPA ' + escapeHTML(workload.hpaName) : '') + '</div>' +
	        '</div>' +
	        '<span class="badge ' + tone(workload.qualification) + '">' + escapeHTML(workload.qualification) + '</span>' +
	      '</button>';
	    }

    function selectWorkload(id, persist) {
      selectedWorkloadId = id;
      [...workloadList.querySelectorAll('.workload-row')].forEach(row => {
        row.classList.toggle('active', row.dataset.workloadId === id);
      });
      const workload = overview.workloads.find(item => item.id === id);
      if (!workload) return;
      detailState.textContent = workload.qualification;
      detailBody.innerHTML = workloadDetailHTML(workload);
      bindTimelineWindowControls();
      fetchTimeline(workload);
	      if (persist) persistSelection(workload);
    }

	    function workloadDetailHTML(workload) {
	      const policy = workload.policy ? workload.policy.namespace + '/' + workload.policy.name : 'none';
	      const telemetry = telemetryLabel(workload);
	      const forecast = workload.forecastMethod ? workload.forecastMethod + (workload.forecastConfidence ? ' ' + workload.forecastConfidence.toFixed(2) : '') : 'not evaluated';
	      const timelineState = timelineStateLabel(workload, timelines[workload.id]);
	      const reasonItems = reasons(workload).map(reason => '<div class="reason-item">' + escapeHTML(reason) + '</div>').join('');
	      const evidenceNotice = evidenceMessage(workload);
	      return '<div class="detail-title">' +
	          '<div>' +
	            '<div class="eyebrow">' + escapeHTML(workload.namespace) + '</div>' +
	            '<h3>' + escapeHTML(workload.name) + '</h3>' +
	            '<p class="w-meta">' + escapeHTML(workload.apiVersion || '') + ' ' + escapeHTML(workload.kind || '') + (workload.hpaName ? ' · HPA ' + escapeHTML(workload.hpaName) : '') + '</p>' +
	          '</div>' +
	          '<span class="badge ' + tone(workload.qualification) + '">' + escapeHTML(workload.qualification) + '</span>' +
	        '</div>' +
	        '<div class="kv-grid">' +
	          '<div class="kv"><span>contract</span><b>' + escapeHTML(contractLabel(workload.scalingContract)) + '</b></div>' +
	          '<div class="kv"><span>policy</span><b>' + escapeHTML(policy) + '</b></div>' +
	          '<div class="kv"><span>telemetry</span><b>' + escapeHTML(telemetry) + '</b></div>' +
	          '<div class="kv"><span>current replicas</span><b>' + escapeHTML(replicasLabel(workload)) + '</b></div>' +
	        '</div>' +
	        '<div class="evidence-card">' +
	          '<div class="timeline-bar">' +
	            '<div class="section-title"><span>Replica timeline</span><span>' + escapeHTML(timelineState) + '</span></div>' +
	            timelineWindowHTML() +
	          '</div>' +
	          graphLegendHTML(timelines[workload.id]) +
	          replicaGraphHTML(workload, timelines[workload.id]) +
	          pressureSummaryHTML(timelines[workload.id]) +
	          '<div class="notice">' + evidenceNotice + '</div>' +
	        '</div>' +
	        '<div>' +
	          '<div class="section-title"><span>Blocking reasons</span><span>' + reasons(workload).length + '</span></div>' +
	          '<div class="reason-list">' + reasonItems + '</div>' +
	        '</div>';
	    }

	    function replicaGraphHTML(workload, timeline) {
	      const history = timelineHistory(workload, timeline);
	      const hasRecommendation = history.some(sample => sample.recommended != null);
	      const values = history.flatMap(sample => [sample.current, sample.recommended]).filter(value => value != null);
	      const maxValue = Math.max(1, ...values, 6);
	      const signalExtent = timelineExtent(history, timeline);
	      const currentPath = timelinePath(history, 'current', maxValue, signalExtent, true);
	      const recommendedPath = hasRecommendation ? timelinePath(history, 'recommended', maxValue, signalExtent, false) : '';
	      const demandPath = signalLinePath(timeline && timeline.demand, signalExtent);
	      const pressureArea = pressureAreaPath(preferredPressureSamples(timeline), signalExtent);
	      const currentPoints = timelinePoints(history, 'current', maxValue, 'point-current', signalExtent);
	      const recommendedPoints = hasRecommendation ? timelinePoints(history, 'recommended', maxValue, 'point-recommended', signalExtent, true) : '';
	      const latest = history[history.length - 1] || {};
	      const latestCurrent = latestFieldValue(history, 'current');
	      const latestRecommended = latestFieldValue(history, 'recommended');
	      const recommendedLabel = latestRecommended == null ? 'recommended: none' : 'recommended: ' + latestRecommended;
	      const recommendedLine = recommendedPath ? '<path class="recommended-line" d="' + recommendedPath + '"></path>' : '';
	      const demandLine = demandPath ? '<path class="demand-line" d="' + demandPath + '"></path>' : '';
	      const pressureShape = pressureArea ? '<path class="pressure-area" d="' + pressureArea + '"></path>' : '';
	      const recommendedLegend = hasRecommendation ? '<circle class="point-recommended" cx="370" cy="24" r="5"></circle><text x="382" y="28">recommended</text>' : '<text x="346" y="28">no recommendation yet</text>';
	      const sourceLabel = timeline && history.length > 1 ? 'Prometheus history' : 'current sample';
	      const startLabel = history.length > 1 ? formatAxisAge(history[0].t, latest.t) : '';
	      return '<svg class="replica-chart" viewBox="0 0 560 190" role="img" aria-label="Current and recommended replica timeline">' +
	        '<line class="grid" x1="70" y1="54" x2="526" y2="54"></line>' +
	        '<line class="grid" x1="70" y1="96" x2="526" y2="96"></line>' +
	        '<line class="axis" x1="70" y1="138" x2="526" y2="138"></line>' +
	        '<line class="axis" x1="70" y1="34" x2="70" y2="138"></line>' +
	        '<text x="68" y="158">' + escapeHTML(startLabel) + '</text>' +
	        '<text x="506" y="158">now</text>' +
	        '<text x="30" y="140">0</text>' +
	        '<text x="24" y="42">' + maxValue + '</text>' +
	        '<circle class="point-current" cx="214" cy="24" r="5"></circle><text x="226" y="28">current: ' + (latestCurrent ?? 'unknown') + '</text>' +
	        recommendedLegend +
	        pressureShape +
	        demandLine +
	        '<path class="current-line" d="' + currentPath + '"></path>' +
	        recommendedLine +
	        currentPoints +
	        recommendedPoints +
	        '<text x="70" y="180">' + escapeHTML(sourceLabel) + '</text>' +
	        '<text x="394" y="180">' + escapeHTML(recommendedLabel) + '</text>' +
	      '</svg>';
	    }

	    function graphLegendHTML(timeline) {
	      return '<div class="graph-legend">' +
	        '<span><i class="legend-swatch replicas"></i>available replicas</span>' +
	        '<span><i class="legend-swatch demand"></i>demand trend, scaled to fit</span>' +
	        '<span><i class="legend-swatch pressure"></i>' + escapeHTML(pressureSignalLabel(timeline)) + '</span>' +
	      '</div>';
	    }

	    function timelineWindowHTML() {
	      return '<div class="timeline-window" role="group" aria-label="timeline window">' +
	        lookbackOptions.map(value =>
	          '<button type="button" data-lookback="' + escapeHTML(value) + '" class="' + (value === selectedLookback ? 'active' : '') + '">' + escapeHTML(value) + '</button>'
	        ).join('') +
	      '</div>';
	    }

	    function pressureSummaryHTML(timeline) {
	      const demand = timeline && Array.isArray(timeline.demand) ? timeline.demand : [];
	      const cpu = timeline && Array.isArray(timeline.cpu) ? timeline.cpu : [];
	      const memory = timeline && Array.isArray(timeline.memory) ? timeline.memory : [];
	      const pressure = cpu.length ? cpu : memory;
	      const pressureLabel = cpu.length ? 'CPU usage/request' : 'Memory usage/request';
	      const pressureUnit = cpu.length || memory.length ? '%' : '';
	      return '<div class="section-title"><span>Demand and resource pressure</span><span>observed</span></div>' +
	        '<div class="evidence-strip">' +
	          metricTileHTML('Demand', latestValue(demand), 'rps', deltaLabel(demand)) +
	          metricTileHTML(pressureLabel, latestValue(pressure), pressureUnit, pressure.length ? peakLabel(pressure, pressureUnit) : 'not available') +
	          metricTileHTML('Replica response', replicaChangeLabel(timeline), '', pressureDriverLabel(timeline)) +
	        '</div>';
	    }

	    function pressureSignalLabel(timeline) {
	      if (timeline && Array.isArray(timeline.cpu) && timeline.cpu.length) return 'CPU usage/request';
	      if (timeline && Array.isArray(timeline.memory) && timeline.memory.length) return 'Memory usage/request';
	      return 'resource pressure';
	    }

	    function metricTileHTML(label, value, unit, detail) {
	      const formatted = value == null ? 'none' : formatMetric(value, unit);
	      return '<div class="evidence-metric"><span>' + escapeHTML(label) + '</span><b>' + escapeHTML(formatted) + '</b><small>' + escapeHTML(detail || '') + '</small></div>';
	    }

	    function timelineStateLabel(workload, timeline) {
	      if (timeline && Array.isArray(timeline.samples) && timeline.samples.length > 0) return 'Prometheus';
	      if (timeline && timeline.unavailableText) return 'waiting';
	      if (workload.recommendationState === 'available') return 'recommended';
	      if (workload.recommendationState === 'suppressed') return 'suppressed';
	      return 'current';
	    }

	    function timelineHistory(workload, timeline) {
	      const samples = timeline && Array.isArray(timeline.samples)
	        ? timeline.samples.map(sample => ({
	            t: Date.parse(sample.timestamp),
	            current: sample.current == null ? null : Number(sample.current),
	            recommended: null
	          })).filter(sample => Number.isFinite(sample.t))
	        : [];
	      if (timeline && timeline.recommendation) {
	        const recommendationTime = Date.parse(timeline.recommendation.timestamp);
	        const recommended = Number(timeline.recommendation.replicas);
	        if (Number.isFinite(recommendationTime) && Number.isFinite(recommended)) {
	          samples.push({ t: recommendationTime, current: null, recommended });
	        }
	      }
	      if (samples.length > 0) return samples.sort((left, right) => left.t - right.t);
	      const timestamp = Date.parse(overview.generatedAt || '') || Date.now();
	      return [{
	        t: timestamp,
	        current: workload.currentReplicas == null ? null : Number(workload.currentReplicas),
	        recommended: workload.recommendedReplicas == null || workload.recommendationState === 'unavailable'
	          ? null
	          : Number(workload.recommendedReplicas)
	      }];
	    }

	    function preferredPressureSamples(timeline) {
	      if (!timeline) return [];
	      if (Array.isArray(timeline.cpu) && timeline.cpu.length) return timeline.cpu;
	      if (Array.isArray(timeline.memory) && timeline.memory.length) return timeline.memory;
	      return [];
	    }

	    function timelineExtent(history, timeline) {
	      const timestamps = history.map(sample => Number(sample.t));
	      for (const series of [timeline && timeline.demand, timeline && timeline.cpu, timeline && timeline.memory]) {
	        if (!Array.isArray(series)) continue;
	        for (const sample of series) {
	          const t = Date.parse(sample.timestamp);
	          if (Number.isFinite(t)) timestamps.push(t);
	        }
	      }
	      const minT = timestamps.length ? Math.min(...timestamps) : Date.now();
	      const maxT = timestamps.length ? Math.max(...timestamps) : minT;
	      return { minT, maxT, span: Math.max(1, maxT - minT) };
	    }

	    function signalLinePath(samples, extent) {
	      const points = normalizedSignalCoordinates(samples, extent, false);
	      if (points.length < 2) return '';
	      return points.map((point, index) => (index === 0 ? 'M' : 'L') + point.x + ' ' + point.y).join(' ');
	    }

	    function pressureAreaPath(samples, extent) {
	      const points = normalizedSignalCoordinates(samples, extent, true);
	      if (points.length < 2) return '';
	      const first = points[0];
	      const last = points[points.length - 1];
	      return points.map((point, index) => (index === 0 ? 'M' : 'L') + point.x + ' ' + point.y).join(' ') +
	        ' L' + last.x + ' 138 L' + first.x + ' 138 Z';
	    }

	    function normalizedSignalCoordinates(samples, extent, clampToOne) {
	      if (!Array.isArray(samples) || samples.length === 0) return [];
	      const values = samples.map(sample => Number(sample.value)).filter(Number.isFinite);
	      if (!values.length) return [];
	      const maxValue = clampToOne ? Math.max(1, ...values) : Math.max(...values, 1);
	      return samples.map(sample => {
	        const t = Date.parse(sample.timestamp);
	        const value = Number(sample.value);
	        if (!Number.isFinite(t) || !Number.isFinite(value)) return null;
	        const normalized = clampToOne ? Math.max(0, Math.min(1, value)) : value / maxValue;
	        return {
	          x: Math.round(70 + ((t - extent.minT) / extent.span) * 456),
	          y: Math.round(138 - normalized * 104)
	        };
	      }).filter(Boolean);
	    }

	    function latestValue(samples) {
	      if (!Array.isArray(samples) || samples.length === 0) return null;
	      const value = Number(samples[samples.length - 1].value);
	      return Number.isFinite(value) ? value : null;
	    }

	    function deltaLabel(samples) {
	      if (!Array.isArray(samples) || samples.length < 2) return 'not enough history';
	      const first = Number(samples[0].value);
	      const last = Number(samples[samples.length - 1].value);
	      if (!Number.isFinite(first) || !Number.isFinite(last)) return 'not enough history';
	      const delta = last - first;
	      const sign = delta >= 0 ? '+' : '';
	      return sign + formatMetric(delta, '') + ' over window';
	    }

	    function peakLabel(samples, unit) {
	      const values = Array.isArray(samples) ? samples.map(sample => Number(sample.value)).filter(Number.isFinite) : [];
	      if (!values.length) return 'not available';
	      return 'peak ' + formatMetric(Math.max(...values), unit);
	    }

	    function replicaChangeLabel(timeline) {
	      const samples = timeline && Array.isArray(timeline.samples) ? timeline.samples : [];
	      if (samples.length < 2) return null;
	      const first = Number(samples[0].current);
	      const last = Number(samples[samples.length - 1].current);
	      if (!Number.isFinite(first) || !Number.isFinite(last)) return null;
	      const delta = last - first;
	      return (delta >= 0 ? '+' : '') + String(Math.round(delta));
	    }

	    function pressureDriverLabel(timeline) {
	      const demand = timeline && Array.isArray(timeline.demand) ? timeline.demand : [];
	      const cpu = timeline && Array.isArray(timeline.cpu) ? timeline.cpu : [];
	      const memory = timeline && Array.isArray(timeline.memory) ? timeline.memory : [];
	      const latestDemand = latestValue(demand);
	      const latestCPU = latestValue(cpu);
	      const latestMemory = latestValue(memory);
	      const parts = [];
	      if (latestDemand != null) parts.push('demand ' + formatMetric(latestDemand, 'rps'));
	      if (latestCPU != null) parts.push('cpu/request ' + formatMetric(latestCPU, '%'));
	      if (latestCPU == null && latestMemory != null) parts.push('memory/request ' + formatMetric(latestMemory, '%'));
	      return parts.length ? parts.join(' / ') : 'waiting for signals';
	    }

	    function formatMetric(value, unit) {
	      if (value == null || !Number.isFinite(Number(value))) return 'none';
	      const number = Number(value);
	      if (unit === '%') return Math.round(number * 100) + '%';
	      if (Math.abs(number) >= 100) return Math.round(number) + (unit ? ' ' + unit : '');
	      if (Math.abs(number) >= 10) return number.toFixed(1).replace(/\.0$/, '') + (unit ? ' ' + unit : '');
	      return number.toFixed(2).replace(/0$/, '').replace(/\.0$/, '') + (unit ? ' ' + unit : '');
	    }

	    async function fetchTimeline(workload) {
	      try {
	        const requestedLookback = lookbackOptions.includes(selectedLookback) ? selectedLookback : '30m';
	        const path = '/api/workloads/' + encodeURIComponent(workload.namespace) + '/' + encodeURIComponent(workload.name) + '/timeline?lookback=' + encodeURIComponent(requestedLookback);
	        const response = await fetch(path, { cache: 'no-store' });
	        if (!response.ok) return;
	        if (requestedLookback !== selectedLookback) return;
	        timelines[workload.id] = await response.json();
	        if (selectedWorkloadId === workload.id) {
	          detailBody.innerHTML = workloadDetailHTML(workload);
	          bindTimelineWindowControls();
	        }
	      } catch {
	        return;
	      }
	    }

	    function bindTimelineWindowControls() {
	      detailBody.querySelectorAll('.timeline-window button').forEach(button => {
	        button.addEventListener('click', () => {
	          const lookback = button.dataset.lookback;
	          if (!lookbackOptions.includes(lookback) || lookback === selectedLookback) return;
	          selectedLookback = lookback;
	          const workload = overview.workloads.find(item => item.id === selectedWorkloadId);
	          if (workload) {
	            timelines[workload.id] = null;
	            detailBody.innerHTML = workloadDetailHTML(workload);
	            bindTimelineWindowControls();
	            fetchTimeline(workload);
	            persistSelection(workload);
	          }
	        });
	      });
	    }

	    function timelinePath(history, field, maxValue, extent, extendSingle) {
	      const points = timelineCoordinates(history, field, maxValue, extent);
	      if (points.length === 0) return '';
	      if (points.length === 1) {
	        if (!extendSingle) return '';
	        const point = points[0];
	        return 'M' + point.x + ' ' + point.y + ' L526 ' + point.y;
	      }
	      return points.map((point, index) => (index === 0 ? 'M' : 'L') + point.x + ' ' + point.y).join(' ');
	    }

	    function timelinePoints(history, field, maxValue, className, extent, showAll) {
	      const points = timelineCoordinates(history, field, maxValue, extent);
	      const visiblePoints = showAll || points.length <= 2 ? points : [points[0], points[points.length - 1]];
	      return visiblePoints
	        .map(point => '<circle class="' + className + '" cx="' + point.x + '" cy="' + point.y + '" r="4"></circle>')
	        .join('');
	    }

	    function latestFieldValue(history, field) {
	      for (let index = history.length - 1; index >= 0; index--) {
	        const value = history[index][field];
	        if (value != null) return value;
	      }
	      return null;
	    }

	    function formatAxisAge(start, end) {
	      const deltaMs = Math.max(0, Number(end) - Number(start));
	      const minutes = Math.max(1, Math.round(deltaMs / 60000));
	      return '-' + minutes + 'm';
	    }

	    function timelineCoordinates(history, field, maxValue, extent) {
	      const samples = history.filter(sample => sample[field] != null);
	      if (samples.length === 0) return [];
	      const minT = extent ? extent.minT : Math.min(...samples.map(sample => Number(sample.t)));
	      const span = extent ? extent.span : Math.max(1, Math.max(...samples.map(sample => Number(sample.t))) - minT);
	      return samples.map(sample => {
	        const x = Math.round(70 + ((Number(sample.t) - minT) / span) * 456);
	        const y = Math.round(138 - (Number(sample[field]) / maxValue) * 104);
	        return { x, y };
	      });
	    }

	    function evidenceMessage(workload) {
	      if (workload.recommendationState === 'available') {
	        return 'Policy status has a recommendation. Review telemetry state, forecast, replica bounds, and reasons before using it.';
	      }
	      if (workload.telemetryState === 'unsupported' || workload.telemetryState === 'unavailable') {
	        if ((workload.telemetryMessage || '').includes('not sufficient')) {
	          return 'Prometheus is wired. The controller is collecting history and withholding recommendations until telemetry coverage is sufficient.';
	        }
	        return 'Prometheus query mapping is missing, so the controller can show current replicas but cannot calculate a recommendation.';
	      }
	      if (workload.scalingContract === 'missing') {
	        return 'Scaling contract is missing. Add an HPA or explicit policy bounds before Skale calculates replica guidance.';
	      }
	      if (workload.scalingContract === 'unsupported') {
	        return 'This workload is visible for inventory, but it is outside the v1 recommendation wedge.';
	      }
	      return 'Recommendation inputs come from policy status, telemetry readiness, forecast confidence, policy bounds, and suppression reasons.';
	    }

	    function persistSelection(workload) {
	      const state = { namespace: workload.namespace, workload: workload.id, lookback: selectedLookback };
	      localStorage.setItem('skale-dashboard-selection', JSON.stringify(state));
	      const params = new URLSearchParams();
	      params.set('ns', state.namespace);
	      params.set('workload', state.workload);
	      if (state.lookback !== '30m') params.set('window', state.lookback);
	      history.replaceState(null, '', '#' + params.toString());
	    }

	    function restoreSelection() {
	      const hash = new URLSearchParams(location.hash.replace(/^#/, ''));
	      let namespace = hash.get('ns');
	      let workload = hash.get('workload');
	      let lookback = hash.get('window');
	      if (!namespace) {
	        try {
	          const saved = JSON.parse(localStorage.getItem('skale-dashboard-selection') || '{}');
	          namespace = saved.namespace;
	          workload = saved.workload;
	          lookback = saved.lookback;
	        } catch {
	          namespace = '';
	          workload = '';
	        }
	      }
	      selectedLookback = lookbackOptions.includes(lookback) ? lookback : '30m';
	      if (namespace && byNamespace[namespace]) {
	        selectNamespace(namespace, workload);
	      }
	    }

	    async function refreshOverview() {
	      if (!selectedNamespace) return;
	      try {
	        const response = await fetch('/api/overview', { cache: 'no-store' });
	        if (!response.ok) return;
	        overview = await response.json();
	        byNamespace = buildNamespaceIndex(overview.workloads || []);
	        if (byNamespace[selectedNamespace]) {
	          selectNamespace(selectedNamespace, selectedWorkloadId);
	        }
	      } catch {
	        return;
	      }
	    }

    function escapeHTML(value) {
      return String(value ?? '').replace(/[&<>"']/g, ch => ({
        '&': '&amp;',
        '<': '&lt;',
        '>': '&gt;',
        '"': '&quot;',
        "'": '&#39;'
      }[ch]));
    }

    nsList.querySelectorAll('.namespace-card').forEach(card => {
      card.addEventListener('click', () => selectNamespace(card.dataset.namespace));
    });
	    restoreSelection();
	    setInterval(refreshOverview, 15000);
  </script>
</body>
</html>`))
