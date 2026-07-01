"use client";

// Skill management (design spec §Piece C). The harness Agent Skills that CRN
// injects into every build live in the `skills` table; this page lists them and
// edits each SKILL.md body. List on the left, editor on the right. Built-in
// skills are editable but not deletable. Mutations PUT/DELETE then refresh().

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import {
  API_BASE,
  skillUploadUrl,
  skillUrl,
  skillVersionsUrl,
} from "../lib/config";
import { useSkills } from "../lib/useSkills";
import type { Skill, SkillVersion } from "../lib/types";
import { fileChanges, lineDiff } from "../lib/linediff";

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

// Short relative label for a version's created_at (e.g. "3h ago", "2d ago").
// Falls back to a compact date past a week.
function formatShortDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const secs = Math.floor((Date.now() - d.getTime()) / 1000);
  if (secs < 60) return "just now";
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d ago`;
  return d.toLocaleDateString("en-GB", {
    day: "2-digit",
    month: "short",
    year: "numeric",
  });
}

export default function SkillsPage() {
  const { skills, error, loading, refresh } = useSkills();

  const [selected, setSelected] = useState<string | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [status, setStatus] = useState<Status>({ kind: "idle" });
  const [busy, setBusy] = useState(false);
  // Which extra file (path) is open in the FILES content editor, if any.
  const [openFile, setOpenFile] = useState<string | null>(null);

  // Version history for the selected skill (newest first) + which version is
  // expanded in the DIFF view. `null` selectedVer collapses the diff.
  const [versions, setVersions] = useState<SkillVersion[]>([]);
  const [selectedVer, setSelectedVer] = useState<number | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

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

  // Load the version history for a given skill name. Clears on failure so a
  // stale list from a previous skill never lingers.
  const loadVersions = useCallback(async (name: string) => {
    try {
      const res = await fetch(skillVersionsUrl(name), { cache: "no-store" });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const json = (await res.json()) as { versions: SkillVersion[] };
      setVersions(json.versions);
    } catch {
      setVersions([]);
    }
  }, []);

  // Fetch versions whenever the selected (persisted) skill changes.
  useEffect(() => {
    setSelectedVer(null);
    if (selectedName === null) {
      setVersions([]);
      return;
    }
    void loadVersions(selectedName);
  }, [selectedName, loadVersions]);

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
      await loadVersions(saved.name);
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

  // Upload a .zip of a skill folder; the server unzips + parses SKILL.md, then
  // returns the resulting Skill. Refresh the list and select the upload.
  async function uploadZip(file: File) {
    setBusy(true);
    setStatus({ kind: "idle" });
    try {
      const form = new FormData();
      form.append("file", file);
      const res = await fetch(skillUploadUrl(), {
        method: "POST",
        body: form,
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const saved = (await res.json()) as Skill;
      await refresh();
      setSelected(saved.name);
      setDraft(draftFromSkill(saved));
      setOpenFile(null);
      await loadVersions(saved.name);
      setStatus({ kind: "ok", text: `Uploaded "${saved.name}".` });
    } catch (e) {
      setStatus({
        kind: "err",
        text: e instanceof Error ? e.message : "upload failed",
      });
    } finally {
      setBusy(false);
    }
  }

  function onFilePicked(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    // Reset the input so re-picking the same file fires change again.
    e.target.value = "";
    if (file) void uploadZip(file);
  }

  // The version currently expanded in the diff view, if any.
  const activeVersion = useMemo(
    () =>
      selectedVer === null
        ? null
        : (versions.find((v) => v.version === selectedVer) ?? null),
    [selectedVer, versions],
  );

  // Diff the selected historical version (old) against the current editor body
  // (new) — this shows what the working copy would change relative to that
  // revision. File add/remove is compared the same direction.
  const diff = useMemo(() => {
    if (activeVersion === null || draft === null) return null;
    return {
      lines: lineDiff(activeVersion.body, draft.body),
      files: fileChanges(activeVersion.files, draft.files),
    };
  }, [activeVersion, draft]);

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
            <div className="skills-head-actions">
              <button
                type="button"
                className="btn btn--ghost btn--sm"
                disabled={busy}
                onClick={() => fileInputRef.current?.click()}
              >
                ⬆ upload zip
              </button>
              <button
                type="button"
                className="btn btn--ghost btn--sm"
                onClick={openNew}
              >
                + new skill
              </button>
              <input
                ref={fileInputRef}
                type="file"
                accept=".zip"
                hidden
                onChange={onFilePicked}
              />
            </div>
          </div>

          {/* Upload feedback stays visible even when no skill is selected. */}
          {draft === null && status.kind === "err" ? (
            <p className="alert">{status.text}</p>
          ) : null}
          {draft === null && status.kind === "ok" ? (
            <p className="notice">{status.text}</p>
          ) : null}

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

                {!draft.isNew ? (
                  <section className="history">
                    <div className="files-head">
                      <span className="field-label">history</span>
                      <span className="editor-meta">
                        {versions.length} version
                        {versions.length === 1 ? "" : "s"}
                      </span>
                    </div>

                    {versions.length === 0 ? (
                      <p className="files-empty">No recorded versions yet.</p>
                    ) : (
                      <ul className="ver-list scroll-thin">
                        {versions.map((v) => {
                          const open = v.version === selectedVer;
                          return (
                            <li key={v.version}>
                              <button
                                type="button"
                                className={`ver-row${
                                  open ? " ver-row--active" : ""
                                }`}
                                onClick={() =>
                                  setSelectedVer(open ? null : v.version)
                                }
                              >
                                <span className="ver-tag">v{v.version}</span>
                                <span className="ver-note">
                                  {v.note || "—"}
                                </span>
                                <span className="ver-date">
                                  {formatShortDate(v.created_at)}
                                </span>
                              </button>
                            </li>
                          );
                        })}
                      </ul>
                    )}

                    {activeVersion !== null && diff !== null ? (
                      <div className="ver-diff">
                        <p className="diff-caption">
                          diff · v{activeVersion.version} → current editor body
                        </p>
                        <pre className="diff mono scroll-thin">
                          {diff.lines.map((ln, i) => (
                            <span
                              key={i}
                              className={
                                ln.kind === "add"
                                  ? "diff-add"
                                  : ln.kind === "del"
                                    ? "diff-del"
                                    : "diff-ctx"
                              }
                            >
                              {ln.kind === "add"
                                ? "+ "
                                : ln.kind === "del"
                                  ? "- "
                                  : "  "}
                              {ln.text}
                              {"\n"}
                            </span>
                          ))}
                        </pre>

                        {diff.files.added.length > 0 ||
                        diff.files.removed.length > 0 ? (
                          <ul className="diff-files">
                            {diff.files.added.map((p) => (
                              <li key={`a-${p}`} className="diff-add">
                                + {p}
                              </li>
                            ))}
                            {diff.files.removed.map((p) => (
                              <li key={`r-${p}`} className="diff-del">
                                - {p}
                              </li>
                            ))}
                          </ul>
                        ) : (
                          <p className="files-empty">No file changes.</p>
                        )}
                      </div>
                    ) : null}
                  </section>
                ) : null}
              </div>
            </>
          )}
        </section>
      </div>
    </main>
  );
}
