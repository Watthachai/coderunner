"use client";

// A Duolingo-style theme switch: light (green-on-white) <-> dark. The actual
// theme is a data-theme attribute on <html>, set BEFORE hydration by the inline
// script in layout.tsx (so there's no flash of the wrong theme). This control
// just reads the current value, flips it, and persists the choice.

import { useEffect, useState } from "react";

const STORAGE_KEY = "crn-theme";
type Theme = "light" | "dark";

export function ThemeToggle() {
  // Start light to match SSR; the effect syncs to the real (script-set) value.
  const [theme, setTheme] = useState<Theme>("light");

  useEffect(() => {
    const current = document.documentElement.dataset.theme;
    setTheme(current === "dark" ? "dark" : "light");
  }, []);

  function toggle() {
    const next: Theme = theme === "dark" ? "light" : "dark";
    setTheme(next);
    document.documentElement.dataset.theme = next;
    try {
      localStorage.setItem(STORAGE_KEY, next);
    } catch {
      // localStorage unavailable (private mode) — theme still applies for the
      // session via the data attribute; it just won't persist.
    }
  }

  const on = theme === "dark";
  return (
    <button
      type="button"
      className={`theme-switch${on ? " theme-switch--on" : ""}`}
      role="switch"
      aria-checked={on}
      aria-label="Toggle dark mode"
      title={on ? "Switch to light" : "Switch to dark"}
      onClick={toggle}
    >
      <span className="theme-switch-track">
        <span className="theme-switch-thumb" aria-hidden>
          {on ? "🌙" : "☀️"}
        </span>
      </span>
    </button>
  );
}
