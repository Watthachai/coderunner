package github

import "testing"

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Cafe Pre-Order":  "cafe-pre-order",
		"  Hello!!World ": "hello-world",
		"":                "project",
		"日本 shop":         "shop",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q)=%q want %q", in, got, want)
		}
	}
}

func TestSlugFromURL(t *testing.T) {
	if got := SlugFromURL("https://github.com/acme/crn-shop-1234abcd.git"); got != "acme/crn-shop-1234abcd" {
		t.Errorf("got %q", got)
	}
	if got := SlugFromURL("not-a-url"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestRepoSlug(t *testing.T) {
	const id = "abcdef12-0000-0000-0000-000000000000"
	if got := RepoSlug("", "", "X", id); got != "" {
		t.Errorf("empty owner must yield empty, got %q", got)
	}
	if got := RepoSlug("acme", "", "Cafe Shop", id); got != "acme/crn-cafe-shop-abcdef12" {
		t.Errorf("got %q", got)
	}
	if got := RepoSlug("acme", "https://github.com/acme/existing.git", "Whatever", id); got != "acme/existing" {
		t.Errorf("stored URL should win, got %q", got)
	}
}
