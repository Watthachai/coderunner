package github

import "strings"

// Slugify lowercases s and collapses non-alphanumerics to single dashes,
// trimming to 40 chars. Empty input yields "project".
func Slugify(s string) string {
	const maxLen = 40
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > maxLen {
		slug = strings.Trim(slug[:maxLen], "-")
	}
	if slug == "" {
		return "project"
	}
	return slug
}

// SlugFromURL parses "owner/name" out of https://github.com/<owner>/<name>.git.
// Returns "" when the URL is empty or not in that shape.
func SlugFromURL(repoURL string) string {
	const prefix = "https://github.com/"
	if !strings.HasPrefix(repoURL, prefix) {
		return ""
	}
	slug := strings.TrimSuffix(strings.TrimPrefix(repoURL, prefix), ".git")
	if strings.Count(slug, "/") != 1 || strings.HasPrefix(slug, "/") || strings.HasSuffix(slug, "/") {
		return ""
	}
	return slug
}

// RepoSlug derives "owner/name" for a project under the "one repo per project"
// model: it prefers the project's stored repo URL and falls back to
// "owner/crn-<slug>-<id8>". Returns "" when owner is empty (model disabled).
func RepoSlug(owner, repoURL, name, id string) string {
	if owner == "" {
		return ""
	}
	if slug := SlugFromURL(repoURL); slug != "" {
		return slug
	}
	idShort := id
	if len(idShort) > 8 {
		idShort = idShort[:8]
	}
	return owner + "/crn-" + Slugify(name) + "-" + idShort
}
