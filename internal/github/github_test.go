package github

import "testing"

func TestIssueNumberFromURL(t *testing.T) {
	n, err := issueNumberFromURL("https://github.com/acme/repo/issues/42")
	if err != nil || n != 42 {
		t.Fatalf("got n=%d err=%v", n, err)
	}
	if _, err := issueNumberFromURL("https://github.com/acme/repo/issues/"); err == nil {
		t.Fatal("expected error on trailing slash")
	}
}

func TestLastLine(t *testing.T) {
	if got := lastLine("warning: something\nhttps://github.com/a/b/issues/7\n"); got != "https://github.com/a/b/issues/7" {
		t.Errorf("got %q", got)
	}
}
