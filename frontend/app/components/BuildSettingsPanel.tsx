"use client";

import { useEffect, useState } from "react";
import { useBuildSettings } from "../lib/useBuildSettings";
import { modelLabel } from "../lib/modelLabel";
import { formatRelative } from "../lib/format";
import { useTick } from "../lib/useTick";

// The Claude model and effort builds run with, and the control that changes
// them. /internal has no auth, so every choice shows its price and the header
// says who changed it last — visibility is the only guardrail.
export function BuildSettingsPanel() {
  const { data, error, saving, save } = useBuildSettings();
  const nowMs = useTick(30_000);
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  const [justSaved, setJustSaved] = useState(false);

  // Follow the server's value when it loads and after each save.
  useEffect(() => {
    if (!data) return;
    setModel(data.model);
    setEffort(data.effort);
  }, [data]);

  if (!data) {
    return (
      <section className="panel">
        <header className="panel-head">
          <h2>Build model</h2>
        </header>
        <p className={error ? "alert" : "empty"}>
          {error ? `Couldn't load build settings — ${error}.` : "Loading…"}
        </p>
      </section>
    );
  }

  const isListed = (id: string) => data.options.models.some((m) => m.id === id);
  const dirty = model !== data.model || effort !== data.effort;
  // An env alias ("opus") is shown but can't be saved — the server would refuse it.
  const canSave = dirty && isListed(model) && !saving;
  const origin =
    data.source === "saved"
      ? `saved ${formatRelative(data.updated_at, nowMs)}${
          data.updated_from ? ` from ${data.updated_from}` : ""
        }`
      : "default from env";

  async function onSave() {
    setJustSaved(false);
    setJustSaved(await save(model, effort));
  }

  return (
    <section className="panel">
      <header className="panel-head">
        <h2>Build model</h2>
        <span className="panel-meta">
          {modelLabel(data.model)} · {data.effort} — {origin}
        </span>
      </header>

      <div className="settings-row">
        <label className="field">
          <span className="field-label">model</span>
          <select
            className="input"
            value={model}
            disabled={saving}
            onChange={(e) => {
              setModel(e.target.value);
              setJustSaved(false);
            }}
          >
            {isListed(data.model) ? null : (
              <option value={data.model}>{data.model} (from env)</option>
            )}
            {data.options.models.map((m) => (
              <option key={m.id} value={m.id}>
                {`${m.label} · $${m.input_usd_per_mtok} in / $${m.output_usd_per_mtok} out per 1M tokens`}
              </option>
            ))}
          </select>
        </label>

        <label className="field field--narrow">
          <span className="field-label">effort</span>
          <select
            className="input"
            value={effort}
            disabled={saving}
            onChange={(e) => {
              setEffort(e.target.value);
              setJustSaved(false);
            }}
          >
            {data.options.efforts.map((e) => (
              <option key={e} value={e}>
                {e}
              </option>
            ))}
          </select>
        </label>

        <button type="button" className="btn" disabled={!canSave} onClick={onSave}>
          {saving ? "saving…" : "save"}
        </button>
      </div>

      <p className="settings-note">
        Applies to the next build that starts its Claude run — a running build
        keeps what it started with.{justSaved ? " Saved." : ""}
      </p>
      {error ? <p className="alert">Couldn't save — {error}.</p> : null}
    </section>
  );
}
