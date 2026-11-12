// Package registrypush is the push helper's whole job: put one image that docker save wrote
// into the delivery registry, under one new tag, with a login that only this helper can read.
//
// CRN runs it through sudo as a user of its own (deploy/macmini/auto-push-setup.sh). CRN's OS
// user is also the agents' user, so whatever reaches the helper is treated as if an agent
// wrote it: the image name, the tag and the layout are all checked, it refuses while an agent
// runs, and it never writes a tag the registry already has. The worst a caller can do is add
// a new tag of a CRN image of a project the helper serves; no existing image is ever changed.
package registrypush

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	ociManifest    = "application/vnd.oci.image.manifest.v1+json"
	dockerManifest = "application/vnd.docker.distribution.manifest.v2+json"

	maxLayout   = 32 << 30 // far more than any app image
	maxMetadata = 1 << 20
	maxConfig   = 4 << 20
)

// AgentRunning is how a refusal for a running agent begins: a caller may try again later.
const AgentRunning = "an agent is running"

// Config is root's: which registry, each served project's login, where the log is.
type Config struct {
	Registry string `json:"registry"` // host:port the images are named with
	Scheme   string `json:"scheme"`   // http for the on-prem registry
	// Logins is each GitLab project the helper serves, group/project, and the file of that
	// project's one login (a docker config, mode 600, the helper's). A push uses the login
	// of its image's project and no other; a project with no login here is refused.
	Logins  map[string]string `json:"logins"`
	Log     string            `json:"log"`      // one line per push or refusal; never a token
	WorkDir string            `json:"work_dir"` // where the layout is unpacked
}

// Result is what a push did: the digest the registry gave it, and which login pushed it.
type Result struct {
	Digest string
	Login  string
}

var projectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(?:/[a-z0-9][a-z0-9._-]*)+$`)

// Deps is what the helper asks of the machine.
type Deps struct {
	// AgentSessions lists every agent process on the machine.
	AgentSessions func() ([]string, error)
	// Caller is who ran the helper (sudo's SUDO_USER), for the log.
	Caller string
}

// LoadConfig reads the config, which only owner (root) may be able to change: the file and
// its directory must be owner's and writable by no one else.
func LoadConfig(file string, owner int) (Config, error) {
	var cfg Config
	for _, p := range []string{filepath.Dir(file), file} {
		info, err := os.Lstat(p)
		if err != nil {
			return cfg, fmt.Errorf("config: %w", err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != owner || info.Mode().Perm()&0o022 != 0 || info.Mode()&fs.ModeSymlink != 0 {
			return cfg, fmt.Errorf("config: %s must be owned by uid %d and writable by no one else", p, owner)
		}
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	if cfg.Registry == "" || len(cfg.Logins) == 0 || cfg.Log == "" || cfg.WorkDir == "" {
		return cfg, errors.New("config: registry, logins, log and work_dir are all needed")
	}
	for project, file := range cfg.Logins {
		if !projectName.MatchString(project) || strings.Contains(project, "..") || !filepath.IsAbs(file) || filepath.Clean(file) != file {
			return cfg, fmt.Errorf("config: login %q = %q is not group/project = an absolute file", project, file)
		}
	}
	if cfg.Scheme != "http" && cfg.Scheme != "https" {
		return cfg, errors.New("config: scheme is http or https")
	}
	return cfg, nil
}

var (
	imageName   = regexp.MustCompile(`^crn-demo-[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	tagName     = regexp.MustCompile(`^v[1-9][0-9]{0,5}$`)
	blobName    = regexp.MustCompile(`^blobs/sha256/([0-9a-f]{64})$`)
	digestValue = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// target is a ref the helper may push: registry/group/project/crn-demo-...:vN.
type target struct {
	ref, project, repository, tag string
}

func parseTarget(cfg Config, ref string) (target, error) {
	host, rest, ok := strings.Cut(ref, "/")
	if !ok || host != cfg.Registry {
		return target{}, fmt.Errorf("%q is not an image of %s", ref, cfg.Registry)
	}
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return target{}, fmt.Errorf("%q has no tag", ref)
	}
	repository, tag := rest[:i], rest[i+1:]
	j := strings.LastIndex(repository, "/")
	if j < 0 {
		return target{}, fmt.Errorf("%q is not group/project/image", ref)
	}
	project, name := repository[:j], repository[j+1:]
	_, served := cfg.Logins[project]
	switch {
	case !served:
		return target{}, fmt.Errorf("project %q is not one this helper serves", project)
	case !imageName.MatchString(name):
		return target{}, fmt.Errorf("image %q is not a CRN image", name)
	case !tagName.MatchString(tag):
		return target{}, fmt.Errorf("tag %q is not a build's tag (v1, v2, ...)", tag)
	}
	return target{ref: ref, project: project, repository: repository, tag: tag}, nil
}

// Push checks everything, then pushes the layout's one image as ref with the login of ref's
// project, and says the digest the registry gave it and which login that was. Every outcome
// is logged; a token never is.
func Push(ctx context.Context, cfg Config, ref string, layout io.Reader, deps Deps) (res Result, err error) {
	defer func() { writeLog(cfg, deps.Caller, ref, res, err) }()
	digest, user, err := push(ctx, cfg, ref, layout, deps)
	if err != nil {
		return Result{Login: user}, err
	}
	return Result{Digest: digest, Login: user}, nil
}

func push(ctx context.Context, cfg Config, ref string, layout io.Reader, deps Deps) (string, string, error) {
	t, err := parseTarget(cfg, ref)
	if err != nil {
		return "", "", refused(err)
	}
	agents, err := deps.AgentSessions()
	if err != nil {
		return "", "", refused(fmt.Errorf("cannot tell whether an agent runs: %w", err))
	}
	if len(agents) > 0 {
		return "", "", refused(fmt.Errorf("%s (%s): CRN pushes only once every agent has stopped", AgentRunning, strings.Join(agents, "; ")))
	}
	user, password, err := readLogin(cfg, t.project)
	if err != nil {
		return "", "", refused(err)
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o700); err != nil {
		return "", user, err
	}
	dir, err := os.MkdirTemp(cfg.WorkDir, "layout-")
	if err != nil {
		return "", user, err
	}
	defer os.RemoveAll(dir)
	img, err := unpack(layout, dir, t.ref)
	if err != nil {
		return "", user, refused(err)
	}
	c := &client{cfg: cfg, user: user, password: password, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if err := c.tagAbsent(ctx, t); err != nil {
		return "", user, err
	}
	for _, d := range img.blobs {
		if err := c.pushBlob(ctx, t, d, filepath.Join(dir, strings.TrimPrefix(d.Digest, "sha256:"))); err != nil {
			return "", user, err
		}
	}
	if err := c.tagAbsent(ctx, t); err != nil {
		return "", user, err
	}
	digest, err := c.putManifest(ctx, t, img)
	return digest, user, err
}

type refusal struct{ err error }

func (r refusal) Error() string { return "refused: " + r.err.Error() }
func (r refusal) Unwrap() error { return r.err }

func refused(err error) error { return refusal{err} }

func writeLog(cfg Config, caller, ref string, res Result, err error) {
	entry := map[string]string{"time": time.Now().UTC().Format(time.RFC3339), "caller": caller, "ref": ref}
	if res.Login != "" {
		entry["login"] = res.Login
	}
	switch {
	case err == nil:
		entry["outcome"], entry["digest"] = "pushed", res.Digest
	case errors.As(err, new(refusal)):
		entry["outcome"], entry["error"] = "refused", err.Error()
	default:
		entry["outcome"], entry["error"] = "failed", err.Error()
	}
	line, _ := json.Marshal(entry)
	f, ferr := os.OpenFile(cfg.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// readLogin reads the login of one project: the helper's own regular file, mode 600, one
// name, holding one login for the registry.
func readLogin(cfg Config, project string) (string, string, error) {
	file, ok := cfg.Logins[project]
	if !ok {
		return "", "", fmt.Errorf("no login for %s", project)
	}
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", "", errors.New("the login file cannot be opened")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 8<<10 {
		return "", "", errors.New("the login file is not a private regular file")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() || st.Nlink != 1 {
		return "", "", errors.New("the login file is not the helper's own")
	}
	var config struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.NewDecoder(io.LimitReader(f, 8<<10)).Decode(&config); err != nil {
		return "", "", errors.New("the login file is not a docker login")
	}
	login, ok := config.Auths[cfg.Registry]
	if !ok || len(config.Auths) != 1 {
		return "", "", errors.New("the login file does not hold one login for this registry")
	}
	plain, err := base64.StdEncoding.DecodeString(login.Auth)
	user, password, found := strings.Cut(string(plain), ":")
	if err != nil || !found || user == "" || password == "" {
		return "", "", errors.New("the login file does not hold a user and a token")
	}
	return user, password, nil
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
}

// unpacked is the one image of a layout: its manifest, and the blobs it needs, in order.
type unpacked struct {
	mediaType string
	manifest  []byte
	digest    string
	blobs     []descriptor
}

// unpack reads docker save's OCI layout into dir, each blob checked against its name as it
// is written, and returns its one image, which must be named ref.
func unpack(layout io.Reader, dir, ref string) (*unpacked, error) {
	tr := tar.NewReader(io.LimitReader(layout, maxLayout))
	meta := map[string][]byte{}
	sizes := map[string]int64{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("the image is not a tar: %w", err)
		}
		name := path.Clean(h.Name)
		switch {
		case h.Typeflag == tar.TypeDir && (name == "blobs" || name == "blobs/sha256"):
		case h.Typeflag == tar.TypeReg && (name == "oci-layout" || name == "index.json" || name == "manifest.json" || name == "repositories"):
			raw, err := io.ReadAll(io.LimitReader(tr, maxMetadata+1))
			if err != nil || len(raw) > maxMetadata {
				return nil, fmt.Errorf("the image's %s cannot be read", name)
			}
			meta[name] = raw
		case h.Typeflag == tar.TypeReg && blobName.MatchString(name):
			hexDigest := blobName.FindStringSubmatch(name)[1]
			n, err := writeBlob(tr, filepath.Join(dir, hexDigest), hexDigest)
			if err != nil {
				return nil, err
			}
			sizes["sha256:"+hexDigest] = n
		default:
			return nil, fmt.Errorf("the image holds %q, which no image layout has", h.Name)
		}
	}
	var index struct {
		Manifests []struct {
			descriptor
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(meta["index.json"], &index); err != nil {
		return nil, errors.New("the image has no readable index.json")
	}
	if len(index.Manifests) != 1 {
		return nil, fmt.Errorf("the layout holds %d images, not one", len(index.Manifests))
	}
	top := index.Manifests[0]
	if top.MediaType != ociManifest && top.MediaType != dockerManifest {
		return nil, fmt.Errorf("the image is a %s, not one platform's manifest", top.MediaType)
	}
	if got := top.Annotations["io.containerd.image.name"]; got != ref {
		return nil, fmt.Errorf("the layout is %q, not %q", got, ref)
	}
	if !digestValue.MatchString(top.Digest) || sizes[top.Digest] != top.Size {
		return nil, errors.New("the image's manifest is not in the layout")
	}
	raw, err := os.ReadFile(filepath.Join(dir, strings.TrimPrefix(top.Digest, "sha256:")))
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil || m.SchemaVersion != 2 || (m.MediaType != "" && m.MediaType != top.MediaType) {
		return nil, errors.New("the image's manifest cannot be read")
	}
	if m.Config.Size > maxConfig {
		return nil, errors.New("the image's config is too large")
	}
	out := &unpacked{mediaType: top.MediaType, manifest: raw, digest: top.Digest}
	seen := map[string]bool{}
	for _, d := range append([]descriptor{m.Config}, m.Layers...) {
		if !digestValue.MatchString(d.Digest) {
			return nil, fmt.Errorf("the manifest names %q", d.Digest)
		}
		size, ok := sizes[d.Digest]
		if !ok {
			return nil, fmt.Errorf("blob %s is not in the layout", d.Digest)
		}
		if size != d.Size {
			return nil, fmt.Errorf("blob %s has %d bytes, the manifest says %d", d.Digest, size, d.Size)
		}
		if !seen[d.Digest] {
			seen[d.Digest] = true
			out.blobs = append(out.blobs, d)
		}
	}
	return out, nil
}

func writeBlob(r io.Reader, file, hexDigest string) (int64, error) {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, fmt.Errorf("blob %s twice in the layout", hexDigest)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if hex.EncodeToString(h.Sum(nil)) != hexDigest {
		return 0, fmt.Errorf("blob %s is not what its name says", hexDigest)
	}
	return n, nil
}

// client speaks the registry's API with the login, and sends the login only to the token
// realm on the registry's own host.
type client struct {
	cfg            Config
	user, password string
	http           *http.Client
	realm, service string
}

func (c *client) base() string { return c.cfg.Scheme + "://" + c.cfg.Registry }

func (c *client) challenge(ctx context.Context) error {
	if c.realm != "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/v2/", nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("the registry does not answer")
	}
	res.Body.Close()
	params := map[string]string{}
	scheme, rest, _ := strings.Cut(res.Header.Get("WWW-Authenticate"), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return errors.New("the registry asks for no token")
	}
	for _, part := range strings.Split(rest, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		params[k] = strings.Trim(v, `"`)
	}
	realm, err := url.Parse(params["realm"])
	registryHost, _, _ := strings.Cut(c.cfg.Registry, ":")
	if err != nil || (realm.Scheme != "http" && realm.Scheme != "https") || realm.Hostname() != registryHost || realm.User != nil {
		return errors.New("the registry sends its login to another host: refused")
	}
	c.realm, c.service = realm.String(), params["service"]
	return nil
}

func (c *client) token(ctx context.Context, repository, actions string) (string, error) {
	if err := c.challenge(ctx); err != nil {
		return "", err
	}
	q := url.Values{"service": {c.service}, "scope": {"repository:" + repository + ":" + actions}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.realm+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.user, c.password)
	res, err := c.http.Do(req)
	if err != nil {
		return "", errors.New("GitLab does not answer for a token")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitLab answered %d for a token", res.StatusCode)
	}
	var answer struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&answer); err != nil {
		return "", errors.New("GitLab's token answer cannot be read")
	}
	if answer.Token == "" {
		answer.Token = answer.AccessToken
	}
	if answer.Token == "" {
		return "", errors.New("GitLab gave no token")
	}
	return answer.Token, nil
}

func (c *client) do(ctx context.Context, method, address, token string, body io.Reader, size int64, header map[string]string) (*http.Response, error) {
	u, err := url.Parse(address)
	if err != nil || u.Host != c.cfg.Registry {
		return nil, errors.New("the registry pointed somewhere else: refused")
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: no answer", method, u.Path)
	}
	return res, nil
}

// tagAbsent is the rule that keeps every existing image as it is.
func (c *client) tagAbsent(ctx context.Context, t target) error {
	token, err := c.token(ctx, t.repository, "pull")
	if err != nil {
		return err
	}
	res, err := c.do(ctx, http.MethodHead, c.base()+"/v2/"+t.repository+"/manifests/"+t.tag, token, nil, 0,
		map[string]string{"Accept": ociManifest + ", " + dockerManifest + ", application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json"})
	if err != nil {
		return err
	}
	res.Body.Close()
	switch res.StatusCode {
	case http.StatusNotFound:
		return nil
	case http.StatusOK:
		return refused(fmt.Errorf("%s is already in the registry, and a tag is never written twice", t.ref))
	default:
		return fmt.Errorf("the registry answered %d about %s", res.StatusCode, t.ref)
	}
}

func (c *client) pushBlob(ctx context.Context, t target, d descriptor, file string) error {
	token, err := c.token(ctx, t.repository, "push,pull")
	if err != nil {
		return err
	}
	blobURL := c.base() + "/v2/" + t.repository + "/blobs/" + d.Digest
	res, err := c.do(ctx, http.MethodHead, blobURL, token, nil, 0, nil)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode == http.StatusOK {
		return nil
	}
	res, err = c.do(ctx, http.MethodPost, c.base()+"/v2/"+t.repository+"/blobs/uploads/", token, nil, 0, nil)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		return fmt.Errorf("the registry answered %d to an upload of %s", res.StatusCode, d.Digest)
	}
	location, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		return errors.New("the registry gave no upload address")
	}
	upload := (&url.URL{Scheme: c.cfg.Scheme, Host: c.cfg.Registry}).ResolveReference(location)
	q := upload.Query()
	q.Set("digest", d.Digest)
	upload.RawQuery = q.Encode()
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	res, err = c.do(ctx, http.MethodPut, upload.String(), token, f, d.Size, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		return fmt.Errorf("the registry answered %d to %s", res.StatusCode, d.Digest)
	}
	return nil
}

func (c *client) putManifest(ctx context.Context, t target, img *unpacked) (string, error) {
	token, err := c.token(ctx, t.repository, "push,pull")
	if err != nil {
		return "", err
	}
	res, err := c.do(ctx, http.MethodPut, c.base()+"/v2/"+t.repository+"/manifests/"+t.tag, token, bytes.NewReader(img.manifest), int64(len(img.manifest)), map[string]string{"Content-Type": img.mediaType})
	if err != nil {
		return "", err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("the registry answered %d to the manifest of %s", res.StatusCode, t.ref)
	}
	if got := res.Header.Get("Docker-Content-Digest"); got != "" && got != img.digest {
		return "", fmt.Errorf("the registry says %s is %s, the image is %s", t.ref, got, img.digest)
	}
	return img.digest, nil
}

// AgentSessions is every agent process on this machine, as push-window.sh looks for them.
func AgentSessions() ([]string, error) {
	out, err := exec.Command("/usr/bin/pgrep", "-fl", `(^|/)(claude|codex|gemini|aider|opencode|cursor-agent|goose|amp)( |$)|claude-code`).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sessions []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if len(line) > 80 {
			line = line[:80]
		}
		sessions = append(sessions, line)
	}
	return sessions, nil
}
