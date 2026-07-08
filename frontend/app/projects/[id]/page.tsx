// Per-project interactive terminal page. Same brand header as the console,
// with a ← console link, the project id in mono, and a large panel hosting the
// live PTY (see components/ProjectTerminal).
//
// Next 15/16: params is a Promise — type as PageProps<'/projects/[id]'> and
// await it (async request APIs are Promises). The terminal itself is a client
// component; this server wrapper just resolves the id and passes it down.

import Link from "next/link";
import { API_BASE } from "../../lib/config";
import { ProjectTerminal } from "../../components/ProjectTerminal";
import { RequestEdit } from "../../components/RequestEdit";
import { ProjectIssues } from "../../components/ProjectIssues";
import { ProjectBuilds } from "../../components/ProjectBuilds";

export default async function ProjectTerminalPage(
  props: PageProps<"/projects/[id]">,
) {
  const { id } = await props.params;

  return (
    <main className="console">
      <header className="page-head">
        <div className="brand">
          <span className="brand-mark" />
          <div>
            <h1>FITT Code Runner</h1>
            <p className="brand-sub">project terminal</p>
          </div>
        </div>
        <div className="head-right">
          <Link className="navlink" href="/">
            ← console
          </Link>
          <code className="api-base">{API_BASE}</code>
        </div>
      </header>

      <div className="term-idline">
        <span className="term-idline-label">project</span>
        <code className="term-id">{id}</code>
      </div>

      <RequestEdit projectId={id} />

      <ProjectIssues projectId={id} />

      <div className="term-panel">
        <ProjectTerminal projectId={id} />
      </div>

      <ProjectBuilds projectId={id} />
    </main>
  );
}
