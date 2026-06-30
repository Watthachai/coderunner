"use client";

// Skill management (design spec §Piece C). The harness Agent Skills that CRN
// injects into every build live in the `skills` table; this page lists them and
// edits each SKILL.md body. List on the left, editor on the right. Built-in
// skills are editable but not deletable. Mutations PUT/DELETE then refresh().

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { API_BASE, skillUrl } from "../lib/config";
import { useSkills } from "../lib/useSkills";
import type { Skill } from "../lib/types";

type Status =
  | { kind: "idle" }
  | { kind: "ok"; text: string }
  | { kind: "err"; text: string };

// Working copy of the editor fields — what the operator is editing right now.
interface Draft {
  name: string;
  description: string;
  body: string;
  enabled: boolean;
  is_builtin: boolean;
  // Extra bundled files keyed by relative path. SKILL.md stays in `body`.
  files: Record<string, string>;
  // `null` name marks the special "New skill" draft (name input is editable).
  isNew: boolean;
}

function draftFromSkill(s: Skill): Draft {
  return {
    name: s.name,
    description: s.description,
    body: s.body,
    enabled: s.enabled,
    is_builtin: s.is_builtin,
    files: { ...s.files },
    isNew: false,
  };
}

function newDraft(): Draft {
  return {
    name: "",
    description: "",
    body: "",
    enabled: true,
    is_builtin: false,
    files: {},
    isNew: true,
  };
}

// A relative path inside the skill bundle is invalid if it is empty, absolute,
// or tries to escape the bundle root via "..". Returns an error string or null.
function badFilePath(path: string): string | null {
  const p = path.trim();
  if (p === "") return "Path is required.";
  if (p.startsWith("/")) return "Path must be relative (no leading /).";
  if (p.split("/").some((seg) => seg === "..")) {
    return 'Path may not contain "..".';
  }
  return null;
}

function formatUpdated(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("en-GB", { hour12: false });
}

export default function SkillsPage() {
  const { skills, error, loading, refresh } = useSkills();

  const [selected, setSelected] = useState<string | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [status, setStatus] = useState<Status>({ kind: "idle" });
  const [busy, setBusy] = useState(false);
  // Which extra file (path) is open in the FILES content editor, if any.
  const [openFile, setOpenFile] = useState<string | null>(null);

  // Auto-select the first skill once the list arrives and nothing is open.
  useEffect(() => {
    if (draft === null && selected === null && skills.length > 0) {
      setSelected(skills[0].name);
      setDraft(draftFromSkill(skills[0]));
    }
  }, [skills, draft, selected]);

  const conn = error ? "down" : loading ? "wait" : "live";
  const connLabel = error ? "disconnected" : loading ? "loading" : "live";

  const selectedName = useMemo(
    () => (draft && !draft.isNew ? draft.name : null),
    [draft],
  );

  function openSkill(s: Skill) {
    setSelected(s.name);
    setDraft(draftFromSkill(s));
    setOpenFile(null);
    setStatus({ kind: "idle" });
  }

  function openNew() {
    setSelected(null);
    setDraft(newDraft());
    setOpenFile(null);
    setStatus({ kind: "idle" });
  }

  function patchDraft(patch: Partial<Draft>) {
    setDraft((d) => (d ? { ...d, ...patch } : d));
  }

  // Sorted paths of the extra files in the current draft.
  const filePaths = useMemo(
    () => (draft ? Object.keys(draft.files).sort() : []),
    [draft],
  );

  // Prompt for a relative path, validate it, then add an empty editable file.
  function addFile() {
    if (!draft) return;
    const input = window.prompt(
      "New file path (relative, e.g. scripts/verify.py):",
      "",
    );
    if (input === null) return; // cancelled
    const path = input.trim();
    const bad = badFilePath(path);
    if (bad !== null) {
      setStatus({ kind: "err", text: bad });
      return;
    }
    if (Object.prototype.hasOwnProperty.call(draft.files, path)) {
      setStatus({ kind: "err", text: `"${path}" already exists.` });
      return;
    }
    patchDraft({ files: { ...draft.files, [path]: "" } });
    setOpenFile(path);
    setStatus({ kind: "idle" });
  }

  function deleteFile(path: string) {
    if (!draft) return;
    const next = { ...draft.files };
    delete next[path];
    patchDraft({ files: next });
    if (openFile === path) setOpenFile(null);
  }

  function setFileContent(path: string, content: string) {
    if (!draft) return;
    patchDraft({ files: { ...draft.files, [path]: content } });
  }

  async function save() {
    if (!draft) return;
    const name = draft.name.trim();
    if (name === "") {
      setStatus({ kind: "err", text: "Name is required." });
      return;
    }
    setBusy(true);
    setStatus({ kind: "idle" });
    try {
      const res = await fetch(skillUrl(name), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          description: draft.description,
          body: draft.body,
          enabled: draft.enabled,
          files: draft.files,
        }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const saved = (await res.json()) as Skill;
      await refresh();
      setSelected(saved.name);
      setDraft(draftFromSkill(saved));
      setOpenFile(null);
      setStatus({ kind: "ok", text: `Saved "${saved.name}".` });
    } catch (e) {
      setStatus({
        kind: "err",
        text: e instanceof Error ? e.message : "save failed",
      });
    } finally {
      setBusy(false);
    }
  }

  // Flip enabled and persist immediately (the spec wants the toggle to save).
  async function toggleEnabled(next: boolean) {
    if (!draft) return;
    patchDraft({ enabled: next });
    if (draft.isNew) return; // not persisted yet; saved on Save
    setBusy(true);
    try {
      const res = await fetch(skillUrl(draft.name), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          description: draft.description,
          body: draft.body,
          enabled: next,
          files: draft.files,
        }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const saved = (await res.json()) as Skill;
      await refresh();
      setDraft(draftFromSkill(saved));
      setStatus({
        kind: "ok",
        text: `${saved.name} ${next ? "enabled" : "disabled"}.`,
      });
    } catch (e) {
      patchDraft({ enabled: !next }); // revert optimistic flip
      setStatus({
        kind: "err",
        text: e instanceof Error ? e.message : "toggle failed",
      });
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!draft || draft.isNew || draft.is_builtin) return;
    const name = draft.name;
    setBusy(true);
    setStatus({ kind: "idle" });
    try {
      const res = await fetch(skillUrl(name), { method: "DELETE" });
      if (!res.ok && res.status !== 204) throw new Error(`HTTP ${res.status}`);
      await refresh();
      setSelected(null);
      setDraft(null);
      setStatus({ kind: "ok", text: `Deleted "${name}".` });
    } catch (e) {
      setStatus({
        kind: "err",
        text: e instanceof Error ? e.message : "delete failed",
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="console">
      <header className="page-head">
        <div className="brand">
          <span className="brand-mark" />
          <div>
            <h1>FITT Code Runner</h1>
            <p className="brand-sub">skill management</p>
          </div>
        </div>
        <div className="head-right">
          <Link className="navlink" href="/">
            ← console
          </Link>
          <span className={`conn conn--${conn}`}>
            <span className="conn-dot" />
            {connLabel}
          </span>
          <code className="api-base">{API_BASE}</code>
        </div>
      </header>

      {error ? (
        <p className="alert">
          Can’t reach the daemon at {API_BASE} — {error}.
        </p>
      ) : null}

      <div className="row skills-layout">
        {/* --- list --- */}
        <section className="panel">
          <div className="panel-head">
            <h2>skills</h2>
            <button
              type="button"
              className="btn btn--ghost btn--sm"
              onClick={openNew}
            >
              + new skill
            </button>
          </div>

          {skills.length === 0 && !loading ? (
            <p className="empty">No skills yet. Create one →</p>
          ) : (
            <ul className="skill-list">
              {skills.map((s) => {
                const active = s.name === selectedName;
                return (
                  <li key={s.name}>
                    <button
                      type="button"
                      className={`skill-row${active ? " skill-row--active" : ""}`}
                      onClick={() => openSkill(s)}
                    >
                      <span className="skill-row-main">
                        <span className="skill-name">{s.name}</span>
                        {s.is_builtin ? (
                          <span className="pill skill-pill">built-in</span>
                        ) : null}
                      </span>
                      <span
                        className={`skill-dot${
                          s.enabled ? " skill-dot--on" : ""
                        }`}
                        title={s.enabled ? "enabled" : "disabled"}
                      />
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </section>

        {/* --- editor --- */}
        <section className="panel">
          {draft === null ? (
            <p className="empty">Select a skill, or create a new one.</p>
          ) : (
            <>
              <div className="panel-head">
                <h2>{draft.isNew ? "new skill" : "edit skill"}</h2>
                {draft.is_builtin ? (
                  <span className="pill skill-pill">built-in</span>
                ) : null}
              </div>

              <div className="editor">
                <label className="field">
                  <span className="field-label">name</span>
                  <input
                    className="input"
                    value={draft.name}
                    placeholder="my-skill"
                    disabled={!draft.isNew || busy}
                    onChange={(e) => patchDraft({ name: e.target.value })}
                  />
                </label>

                <label className="field">
                  <span className="field-label">description</span>
                  <input
                    className="input"
                    value={draft.description}
                    placeholder="One line on what this skill does."
                    disabled={busy}
                    onChange={(e) =>
                      patchDraft({ description: e.target.value })
                    }
                  />
                </label>

                <label className="field">
                  <span className="field-label">SKILL.md body</span>
                  <textarea
                    className="input mono skill-body scroll-thin"
                    value={draft.body}
                    placeholder="# my-skill&#10;&#10;Instructions for the build harness…"
                    spellCheck={false}
                    disabled={busy}
                    onChange={(e) => patchDraft({ body: e.target.value })}
                  />
                </label>

                <div className="files">
                  <div className="files-head">
                    <span className="field-label">files</span>
                    <button
                      type="button"
                      className="btn btn--ghost btn--sm"
                      disabled={busy}
                      onClick={addFile}
                    >
                      + add file
                    </button>
                  </div>

                  {filePaths.length === 0 ? (
                    <p className="files-empty">
                      No extra files. SKILL.md only.
                    </p>
                  ) : (
                    <ul className="file-list">
                      {filePaths.map((path) => {
                        const active = path === openFile;
                        return (
                          <li key={path}>
                            <button
                              type="button"
                              className={`file-row${
                                active ? " file-row--active" : ""
                              }`}
                              onClick={() =>
                                setOpenFile(active ? null : path)
                              }
                            >
                              <span className="file-path">{path}</span>
                            </button>
                            <button
                              type="button"
                              className="file-del"
                              title={`delete ${path}`}
                              disabled={busy}
                              onClick={() => deleteFile(path)}
                            >
                              ×
                            </button>
                          </li>
                        );
                      })}
                    </ul>
                  )}

                  {openFile !== null ? (
                    <label className="field file-editor">
                      <span className="field-label">{openFile}</span>
                      <textarea
                        className="input mono file-body scroll-thin"
                        value={draft.files[openFile] ?? ""}
                        placeholder="File contents…"
                        spellCheck={false}
                        disabled={busy}
                        onChange={(e) =>
                          setFileContent(openFile, e.target.value)
                        }
                      />
                    </label>
                  ) : null}
                </div>

                <div className="editor-row">
                  <label className="toggle">
                    <input
                      type="checkbox"
                      checked={draft.enabled}
                      disabled={busy}
                      onChange={(e) => void toggleEnabled(e.target.checked)}
                    />
                    <span className="field-label">enabled</span>
                  </label>

                  {!draft.isNew ? (
                    <span className="editor-meta">
                      updated{" "}
                      {formatUpdated(
                        skills.find((s) => s.name === draft.name)?.updated_at ??
                          "",
                      )}
                    </span>
                  ) : null}
                </div>

                {status.kind === "err" ? (
                  <p className="alert">{status.text}</p>
                ) : null}
                {status.kind === "ok" ? (
                  <p className="notice">{status.text}</p>
                ) : null}

                <div className="editor-actions">
                  <button
                    type="button"
                    className="btn"
                    disabled={busy}
                    onClick={() => void save()}
                  >
                    {draft.isNew ? "Create" : "Save"}
                  </button>
                  {!draft.isNew && !draft.is_builtin ? (
                    <button
                      type="button"
                      className="btn btn--ghost btn--danger"
                      disabled={busy}
                      onClick={() => void remove()}
                    >
                      Delete
                    </button>
                  ) : null}
                </div>
              </div>
            </>
          )}
        </section>
      </div>
    </main>
  );
}
