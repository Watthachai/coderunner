import { API_BASE } from "./lib/config";
import { JobMonitor } from "./components/JobMonitor";
import { ProjectStatusPanel } from "./components/ProjectStatusPanel";

// CRN Dashboard (CRN-architecture.md §2.4).
//
// This is the operator console for the Go daemon. Two live tools are wired:
//   - Project Status: GET /api/v1/projects/{id}/status (read model)
//   - Live Job Monitor: WebSocket /api/v1/projects/{id}/jobs/{build_no}/logs,
//     rendering the streamed BuildEventMsg feed in real time.
//
// There is no list-projects REST endpoint in the backend contract yet, so the
// operator supplies a project id. When that endpoint lands, the Project Status
// panel becomes a list with per-row badges + a "watch" action into the monitor.
export default function Home() {
  return (
    <main className="page">
      <header className="page-head">
        <div className="brand">
          <span className="brand-mark" />
          <div>
            <h1>FITT Code Runner</h1>
            <p className="brand-sub">build daemon · operator dashboard</p>
          </div>
        </div>
        <code className="api-base">{API_BASE}</code>
      </header>

      <div className="grid">
        <ProjectStatusPanel />
        <JobMonitor />
      </div>
    </main>
  );
}
