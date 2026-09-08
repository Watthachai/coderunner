"use client";

import { useHealth } from "../lib/useHealth";

/**
 * BuildStamp shows the commit the daemon is running, beside the API base it is
 * running at.
 *
 * CRN deploys by `git pull` + restart onto boxes that are told apart by IP, and
 * nothing in the console said which commit landed — confirming a deploy meant
 * an ssh session or a curl. The stamp is Go's own VCS record, so it cannot
 * drift from the binary.
 *
 * "dirty" means the binary was built from a working tree with uncommitted
 * changes: whatever is running is not any commit, and cannot be reproduced from
 * the remote.
 */
export function BuildStamp() {
  const build = useHealth();
  if (!build) return null;

  // `go run` carries no VCS stamp, which is how `make run` starts the daemon in
  // dev — so "unknown" is the normal local case, not a fault. Say what it means
  // rather than showing a bare "unknown" that reads like a bug.
  const unstamped = build.revision === "unknown";
  const when = build.time ? new Date(build.time).toLocaleString() : "";
  const title = unstamped
    ? "no VCS stamp — started with `go run` (use make run-bin / restart for a stamped build)"
    : build.modified
      ? `running ${build.revision} (${when}) — built from uncommitted changes`
      : `running commit ${build.revision}${when ? ` (${when})` : ""}`;

  const flagged = unstamped || build.modified;

  return (
    <code
      className={`api-base build-stamp${flagged ? " build-stamp--dirty" : ""}`}
      title={title}
    >
      {unstamped ? "unstamped" : build.revision}
      {build.modified ? " dirty" : ""}
    </code>
  );
}
