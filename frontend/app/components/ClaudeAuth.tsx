import type { ClaudeAuthStatus } from "../lib/types";

// Every build runs Claude Code on the build machine's login. The chip shows it
// at a glance; the banner says what is wrong and what to do, before a build
// fails on it (CRN checks every 15 minutes and before each build).

const time = (iso?: string) =>
  iso ? new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "—";
const date = (iso?: string) => (iso ? iso.slice(0, 10) : "");

function chipState(a: ClaudeAuthStatus | null | undefined): { tone: string; label: string } {
  if (!a || a.state === "unknown") return { tone: "unknown", label: "checking" };
  if (a.state === "ok") return a.expiry_warning ? { tone: "warn", label: `token ${a.days_left}d` } : { tone: "ok", label: "ready" };
  return { tone: "bad", label: a.state === "logged_out" ? "logged out" : a.state.replace("_", " ") };
}

export function ClaudeAuthChip({ auth }: { auth: ClaudeAuthStatus | null | undefined }) {
  const { tone, label } = chipState(auth);
  const title = auth
    ? `Claude Code login · checked ${time(auth.checked_at)}` +
      (auth.probe_at ? ` · last request ${auth.probe_ok ? "accepted" : "refused"} at ${time(auth.probe_at)}` : "") +
      (auth.token_expires_at ? ` · token expires ${date(auth.token_expires_at)}` : "")
    : "Claude Code login";
  return (
    <span className={`claude-chip claude-chip--${tone}`} title={title}>
      <span className="claude-chip-dot" />
      claude · {label}
    </span>
  );
}

export function ClaudeAuthBanner({ auth }: { auth: ClaudeAuthStatus | null | undefined }) {
  if (!auth || auth.state === "unknown") return null;
  const checked = `checked ${time(auth.checked_at)}`;
  if (auth.state === "ok") {
    if (!auth.expiry_warning) return null;
    return (
      <p className="claude-banner claude-banner--warn" role="status">
        The Claude Code token expires in {auth.days_left} days ({date(auth.token_expires_at)}). Before then, run{" "}
        <code>claude setup-token</code> on the build machine, put the new token in CRN&apos;s <code>.env</code> as{" "}
        <code>CLAUDE_CODE_OAUTH_TOKEN</code>, update <code>CRN_CLAUDE_TOKEN_EXPIRES</code> and restart CRN.
      </p>
    );
  }
  const what: Record<string, string> = {
    logged_out: "Claude Code is logged out on the build machine.",
    token_expired: `The Claude Code token expired on ${date(auth.token_expires_at)}.`,
    probe_failed: "Claude Code's last request was refused.",
    error: "CRN can't read the Claude Code login.",
  };
  return (
    <p className="claude-banner claude-banner--bad" role="alert">
      <strong>{what[auth.state] ?? `Claude Code login: ${auth.state}.`}</strong> Builds will fail until it is fixed: log in on
      the build machine (<code>claude</code> then <code>/login</code>) or set a token from <code>claude setup-token</code>{" "}
      as <code>CLAUDE_CODE_OAUTH_TOKEN</code>.
      {auth.detail && (auth.state === "probe_failed" || auth.state === "error") ? (
        <span className="claude-banner-detail"> {auth.detail}</span>
      ) : null}
      <span className="claude-banner-detail"> · {checked}</span>
    </p>
  );
}
