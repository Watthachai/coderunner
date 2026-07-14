package buildstep

import (
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// fitt-feedback.js is the self-contained in-demo feedback widget, inlined into
// every build's HTML so each demo ships a "💬 feedback" control deterministically
// (never left to Claude to reproduce).
//
//go:embed fitt-feedback.js
var feedbackWidgetJS string

// InjectFeedbackWidget inlines the feedback widget into every index-style HTML
// file under dir (before </body>), configured with this project's id and the
// PostgREST ingest URL the widget POSTs to. It is deterministic and best-effort:
// it returns the number of HTML files patched. A missing </body> gets the script
// appended. Skips .git / node_modules / .claude and already-injected files.
//
// Next.js App Router apps have no index.html (app/layout.tsx owns <body>); for
// those it falls back to dropping the widget in public/ and adding a <script src>
// to the layout, so CRN's real (Next.js) output gets the widget too.
func InjectFeedbackWidget(dir, projectID, ingestURL string, logger *slog.Logger) (int, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if ingestURL == "" {
		return 0, nil // no ingest configured — nothing to inject
	}

	// 1. Static/HTML apps: inline the widget before </body> in every HTML file.
	patched, err := injectIntoHTML(dir, feedbackScriptTag(projectID, ingestURL), logger)
	if err != nil {
		return patched, err
	}
	if patched > 0 {
		return patched, nil
	}

	// 2. Next.js App Router (no index.html): public/ + a <script src> in layout.
	return injectIntoNextLayout(dir, projectID, ingestURL, logger), nil
}

// injectIntoHTML inlines tag before </body> in every .html file under dir,
// returning the number of files patched.
func injectIntoHTML(dir, tag string, logger *slog.Logger) (int, error) {
	patched := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries rather than abort the build
		}
		if d.IsDir() {
			switch d.Name() {
			// Skip build OUTPUT dirs: patching prerendered .html there is useless
			// (gitignored, regenerated) AND, because injectIntoHTML returns early
			// once anything is patched, it would suppress the Next-layout injection
			// that actually ships the widget. `.next` is the common culprit.
			case ".git", "node_modules", ".claude", "dist", "build", ".next", "out", ".turbo", ".vercel":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".html") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		html := string(raw)
		if strings.Contains(html, "data-fitt-feedback") {
			return nil // already injected
		}
		// Insert before the closing </body> (case-insensitive), preserving the
		// original tag's case by splitting the untouched source at that index.
		var out string
		if i := strings.LastIndex(strings.ToLower(html), "</body>"); i >= 0 {
			out = html[:i] + tag + "\n" + html[i:]
		} else {
			out = html + "\n" + tag + "\n"
		}
		if werr := os.WriteFile(path, []byte(out), 0o644); werr != nil {
			logger.Warn("write feedback-injected html failed", "path", path, "err", werr)
			return nil
		}
		patched++
		return nil
	})
	if err != nil {
		return patched, fmt.Errorf("buildstep: inject feedback widget: %w", err)
	}
	return patched, nil
}

// injectIntoNextLayout handles Next.js App Router apps: it writes the widget to
// public/fitt-feedback.js and inserts a <script src> before </body> in the root
// layout (app/layout.tsx or src/app/layout.tsx). Returns 1 on success, else 0.
func injectIntoNextLayout(dir, projectID, ingestURL string, logger *slog.Logger) int {
	candidates := []string{
		filepath.Join(dir, "app", "layout.tsx"),
		filepath.Join(dir, "app", "layout.jsx"),
		filepath.Join(dir, "src", "app", "layout.tsx"),
		filepath.Join(dir, "src", "app", "layout.jsx"),
	}
	layoutPath := ""
	for _, c := range candidates {
		if info, statErr := os.Stat(c); statErr == nil && !info.IsDir() {
			layoutPath = c
			break
		}
	}
	if layoutPath == "" {
		return 0
	}
	raw, err := os.ReadFile(layoutPath)
	if err != nil {
		return 0
	}
	src := string(raw)
	if strings.Contains(src, "fitt-feedback") {
		return 0 // already injected
	}
	i := strings.Index(src, "</body>")
	if i < 0 {
		return 0 // no <body> to anchor the script to
	}

	pub := filepath.Join(dir, "public")
	if err := os.MkdirAll(pub, 0o755); err != nil {
		logger.Warn("create public dir failed", "err", err)
		return 0
	}
	if err := os.WriteFile(filepath.Join(pub, "fitt-feedback.js"), []byte(feedbackWidgetJS), 0o644); err != nil {
		logger.Warn("write public/fitt-feedback.js failed", "err", err)
		return 0
	}

	// A plain classic <script src> (not next/script) so document.currentScript and
	// its data-* config resolve during synchronous execution. Values are safe
	// (uuid + url) so no JSX-entity escaping is needed.
	script := `<script src="/fitt-feedback.js" data-fitt-feedback="true" data-project="` +
		projectID + `" data-ingest="` + ingestURL + `" />`
	out := src[:i] + "        " + script + "\n      " + src[i:]
	if err := os.WriteFile(layoutPath, []byte(out), 0o644); err != nil {
		logger.Warn("write feedback-injected layout failed", "path", layoutPath, "err", err)
		return 0
	}
	logger.Info("injected feedback widget into next layout", "layout", layoutPath)
	return 1
}

// feedbackScriptTag wraps the embedded widget in an inline <script> carrying the
// per-build config the widget reads from document.currentScript.dataset.
func feedbackScriptTag(projectID, ingestURL string) string {
	return `<script data-fitt-feedback data-project="` + htmlAttrEscape(projectID) +
		`" data-ingest="` + htmlAttrEscape(ingestURL) + `">` + "\n" +
		feedbackWidgetJS + "\n</script>"
}

func htmlAttrEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;",
	).Replace(s)
}
