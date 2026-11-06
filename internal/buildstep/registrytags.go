package buildstep

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RegistryTags is every tag of every image of one GitLab project: image path, tag, digest.
type RegistryTags map[string]map[string]string

// ImageRef is an image tag taken apart for GitLab, as host/group/project/image:tag.
type ImageRef struct {
	// API is the GitLab behind the registry, which answers on port 80.
	API string
	// Project is group/project, the unit GitLab gives registry rights to.
	Project string
	// Repository is group/project/image.
	Repository string
	Tag        string
}

// ParseImageRef reads an image tag this build pushes.
func ParseImageRef(appTag string) (ImageRef, error) {
	host, rest, ok := strings.Cut(appTag, "/")
	i := strings.LastIndex(rest, ":")
	j := strings.LastIndex(rest, "/")
	if !ok || i < 0 || j < 0 || i < j {
		return ImageRef{}, fmt.Errorf("image tag %q is not host/group/project/image:tag", appTag)
	}
	hostname, _, _ := strings.Cut(host, ":")
	return ImageRef{API: "http://" + hostname, Project: rest[:j], Repository: rest[:i], Tag: rest[i+1:]}, nil
}

var gitlabClient = &http.Client{Timeout: 20 * time.Second}

func gitlabGet(ctx context.Context, address string, into any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	res, err := gitlabClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ask GitLab: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil, fmt.Errorf("ask GitLab: %s answered %d", res.Request.URL.Path, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		return nil, fmt.Errorf("ask GitLab: %s: %w", res.Request.URL.Path, err)
	}
	return res.Header, nil
}

// gitlabList reads a whole list, page by page, until GitLab says that was the last page. An
// answer that does not say so is an error, since a shorter list would read as nothing changed.
func gitlabList[T any](ctx context.Context, address string) ([]T, error) {
	var all []T
	for page := 1; ; page++ {
		var items []T
		header, err := gitlabGet(ctx, address+"?per_page=100&page="+strconv.Itoa(page), &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		next, said := header["X-Next-Page"]
		if !said || len(next) != 1 {
			return nil, errors.New("ask GitLab: it did not say whether another page follows")
		}
		if next[0] == "" {
			if total := header.Get("X-Total"); total != "" && total != strconv.Itoa(len(all)) {
				return nil, fmt.Errorf("ask GitLab: it counts %s entries and listed %d", total, len(all))
			}
			return all, nil
		}
		if next[0] != strconv.Itoa(page+1) || len(items) == 0 {
			return nil, errors.New("ask GitLab: its pages do not follow one another")
		}
	}
}

// ReadRegistryTags asks GitLab's API for all of it, without a login. The project's registry
// is readable by anyone who can reach it. Each image's tags come from that image's own list,
// page by page: a list carried inside the list of images can be cut short. An answer it cannot
// read, or one that names an image or a tag twice, is an error, never a shorter list.
func ReadRegistryTags(ctx context.Context, ref ImageRef) (RegistryTags, error) {
	base := ref.API + "/api/v4/projects/" + url.PathEscape(ref.Project) + "/registry/repositories"
	type repository struct {
		ID   int64  `json:"id"`
		Path string `json:"path"`
	}
	type tag struct {
		Name string `json:"name"`
	}
	repositories, err := gitlabList[repository](ctx, base)
	if err != nil {
		return nil, err
	}
	all := RegistryTags{}
	ids := map[int64]bool{}
	for _, r := range repositories {
		if r.ID <= 0 || r.Path == "" {
			return nil, errors.New("ask GitLab: an image with no id or no path")
		}
		if ids[r.ID] {
			return nil, fmt.Errorf("ask GitLab: it lists image id %d twice", r.ID)
		}
		if _, repeated := all[r.Path]; repeated {
			return nil, fmt.Errorf("ask GitLab: it lists image %s twice", r.Path)
		}
		ids[r.ID] = true
		listed, err := gitlabList[tag](ctx, fmt.Sprintf("%s/%d/tags", base, r.ID))
		if err != nil {
			return nil, err
		}
		tags := map[string]string{}
		for _, t := range listed {
			if t.Name == "" {
				return nil, fmt.Errorf("ask GitLab: a tag of %s with no name", r.Path)
			}
			if _, repeated := tags[t.Name]; repeated {
				return nil, fmt.Errorf("ask GitLab: it lists %s:%s twice", r.Path, t.Name)
			}
			var detail struct {
				Digest string `json:"digest"`
			}
			if _, err := gitlabGet(ctx, fmt.Sprintf("%s/%d/tags/%s", base, r.ID, url.PathEscape(t.Name)), &detail); err != nil {
				return nil, err
			}
			if detail.Digest == "" {
				return nil, fmt.Errorf("ask GitLab: no digest for %s:%s", r.Path, t.Name)
			}
			tags[t.Name] = detail.Digest
		}
		all[r.Path] = tags
	}
	return all, nil
}

// CompareRegistryTags is whether after is before plus exactly the tag this build pushed: an
// empty digest means it pushed nothing. It names each difference, and any other change is
// an error, whoever made it.
func CompareRegistryTags(before, after RegistryTags, repository, tag, digest string) error {
	var problems []string
	seen := map[string]bool{}
	for repo := range before {
		seen[repo] = true
	}
	for repo := range after {
		seen[repo] = true
	}
	for repo := range seen {
		if _, ok := before[repo]; !ok && !(repo == repository && digest != "") {
			problems = append(problems, "new image "+repo)
		}
		if _, ok := after[repo]; !ok {
			problems = append(problems, "image gone "+repo)
			continue
		}
		for name, value := range before[repo] {
			got, ok := after[repo][name]
			switch {
			case !ok:
				problems = append(problems, "tag gone "+repo+":"+name)
			case got != value:
				problems = append(problems, "tag changed "+repo+":"+name)
			}
		}
		for name, value := range after[repo] {
			if _, existed := before[repo][name]; existed {
				continue
			}
			if repo != repository || name != tag || digest == "" {
				problems = append(problems, "new tag "+repo+":"+name)
			} else if value != digest {
				problems = append(problems, "the pushed tag "+repo+":"+name+" is not the pushed digest")
			}
		}
	}
	if digest != "" {
		if got := after[repository][tag]; got == "" {
			problems = append(problems, "the pushed tag "+repository+":"+tag+" is not there")
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return errors.New("the registry changed: " + strings.Join(problems, "; "))
}

// RegistryLoginCanPush asks GitLab whether a login may still write the repository. It sends
// no image and changes nothing. A refused login, or one that gets no push, cannot; no
// answer is an error, since nothing is known.
func RegistryLoginCanPush(ctx context.Context, ref ImageRef, login string) (bool, error) {
	address := ref.API + "/jwt/auth?service=container_registry&scope=" + url.QueryEscape("repository:"+ref.Repository+":push,pull")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return false, err
	}
	user, password, _ := strings.Cut(login, ":")
	req.SetBasicAuth(user, password)
	res, err := gitlabClient.Do(req)
	if err != nil {
		return false, errors.New("ask GitLab about the login: no answer")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return false, nil
	}
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("ask GitLab about the login: it answered %d", res.StatusCode)
	}
	var answer struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&answer); err != nil {
		return false, errors.New("ask GitLab about the login: unreadable answer")
	}
	parts := strings.Split(answer.Token, ".")
	if len(parts) != 3 {
		return false, errors.New("ask GitLab about the login: unreadable answer")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false, errors.New("ask GitLab about the login: unreadable answer")
	}
	var claims struct {
		Access []struct {
			Actions []string `json:"actions"`
		} `json:"access"`
	}
	if err := json.Unmarshal(body, &claims); err != nil {
		return false, errors.New("ask GitLab about the login: unreadable answer")
	}
	for _, entry := range claims.Access {
		for _, action := range entry.Actions {
			if action == "push" {
				return true, nil
			}
		}
	}
	return false, nil
}
