/**
 * Human label for a Claude model id: "claude-opus-5-5" -> "Opus 5.5",
 * "claude-fable-5-1" -> "Fable 5.1", "claude-sonnet-5" -> "Sonnet 5".
 * Anything not shaped "claude-<family>-<n>[-<n>]" comes back unchanged, so an
 * env alias like "opus" still shows as itself.
 *
 * No imports, so it runs under `node --test` without a bundler.
 */
export function modelLabel(id: string): string {
  const m = /^claude-([a-z]+)-(\d+)(?:-(\d+))?$/.exec(id);
  if (!m) return id;
  const family = m[1].charAt(0).toUpperCase() + m[1].slice(1);
  return m[3] === undefined ? `${family} ${m[2]}` : `${family} ${m[2]}.${m[3]}`;
}

/** "Opus 5.5 · xhigh"; "" when the build recorded no model. */
export function claudeLabel(model?: string, effort?: string): string {
  if (!model) return "";
  return effort ? `${modelLabel(model)} · ${effort}` : modelLabel(model);
}
