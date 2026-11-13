package registrypush

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The helper is the only holder of the registry login. It is run, through sudo, by CRN's
// user, which is also the agents' user: so everything it is given is checked as if an agent
// wrote it, and it can only ever add one new tag of one image of the projects it serves.

const (
	testUser      = "gitlab+deploy-token-3"
	testPassword  = "gldt-test-secret-uat"
	demosUser     = "gitlab+deploy-token-1"
	demosPassword = "gldt-test-secret-demos"
)

// fakeLogins is what the fake GitLab knows: each deploy token reaches its own project only.
var fakeLogins = map[string]struct{ password, project string }{
	testUser:  {testPassword, "fitt/ftcdv-uat"},
	demosUser: {demosPassword, "fitt/demos"},
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fakeRegistry is GitLab's registry as far as a push goes: token auth from a realm on the
// same host, blob uploads, and manifests by tag.
type fakeRegistry struct {
	t        *testing.T
	srv      *httptest.Server
	realm    string // overrides where the challenge sends the client
	mu       sync.Mutex
	blobs    map[string][]byte
	tags     map[string]map[string][]byte // repository -> tag -> manifest
	requests []string
	basic    []string // every Authorization: Basic the server saw, decoded
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	r := &fakeRegistry{t: t, blobs: map[string][]byte{}, tags: map[string]map[string][]byte{}}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *fakeRegistry) host() string { u, _ := url.Parse(r.srv.URL); return u.Host }

func (r *fakeRegistry) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req.Method+" "+req.URL.Path)
	if user, pass, ok := req.BasicAuth(); ok {
		r.basic = append(r.basic, user+":"+pass)
	}
	if req.URL.Path == "/jwt/auth" {
		user, pass, ok := req.BasicAuth()
		login, known := fakeLogins[user]
		if !ok || !known || pass != login.password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		scope := req.URL.Query().Get("scope")
		if !strings.HasPrefix(scope, "repository:"+login.project+"/") {
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "none"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "bearer-" + scope})
		return
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer bearer-") {
		realm := r.realm
		if realm == "" {
			realm = r.srv.URL + "/jwt/auth"
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`",service="container_registry"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(req.URL.Path, "/v2/")
	switch {
	case strings.Contains(path, "/blobs/uploads/"):
		repo := path[:strings.Index(path, "/blobs/uploads/")]
		if req.Method == http.MethodPost {
			w.Header().Set("Location", "/v2/"+repo+"/blobs/uploads/session-1?_state=x")
			w.WriteHeader(http.StatusAccepted)
			return
		}
		body, _ := io.ReadAll(req.Body)
		want := req.URL.Query().Get("digest")
		if digestOf(body) != want {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		r.blobs[want] = body
		w.WriteHeader(http.StatusCreated)
	case strings.Contains(path, "/blobs/"):
		d := path[strings.LastIndex(path, "/")+1:]
		if _, ok := r.blobs[d]; ok {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case strings.Contains(path, "/manifests/"):
		i := strings.Index(path, "/manifests/")
		repo, tag := path[:i], path[i+len("/manifests/"):]
		switch req.Method {
		case http.MethodGet, http.MethodHead:
			if _, ok := r.tags[repo][tag]; ok {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			body, _ := io.ReadAll(req.Body)
			var m manifest
			if err := json.Unmarshal(body, &m); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for _, d := range append([]descriptor{m.Config}, m.Layers...) {
				if _, ok := r.blobs[d.Digest]; !ok {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
			}
			if r.tags[repo] == nil {
				r.tags[repo] = map[string][]byte{}
			}
			r.tags[repo][tag] = body
			w.Header().Set("Docker-Content-Digest", digestOf(body))
			w.WriteHeader(http.StatusCreated)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (r *fakeRegistry) wrote() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, q := range r.requests {
		if strings.HasPrefix(q, "PUT ") || strings.HasPrefix(q, "POST ") || strings.HasPrefix(q, "PATCH ") || strings.HasPrefix(q, "DELETE ") {
			out = append(out, q)
		}
	}
	return out
}

// image is what docker save writes for one image: an OCI layout in a tar.
type image struct {
	ref      string
	config   []byte
	layers   [][]byte
	manifest []byte
	// edit changes the tar's entries before it is written: name -> body.
	edit func(entries map[string][]byte)
}

func newImage(ref string) *image {
	img := &image{ref: ref, config: []byte(`{"architecture":"amd64","os":"linux"}`), layers: [][]byte{[]byte("layer one"), []byte("layer two")}}
	m := manifest{SchemaVersion: 2, MediaType: ociManifest, Config: descriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: digestOf(img.config), Size: int64(len(img.config))}}
	for _, l := range img.layers {
		m.Layers = append(m.Layers, descriptor{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: digestOf(l), Size: int64(len(l))})
	}
	img.manifest, _ = json.Marshal(m)
	return img
}

func (img *image) tar(t *testing.T) io.Reader {
	t.Helper()
	index, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": []any{map[string]any{
			"mediaType": ociManifest, "digest": digestOf(img.manifest), "size": len(img.manifest),
			"annotations": map[string]string{"io.containerd.image.name": img.ref, "org.opencontainers.image.ref.name": "v5"},
		}},
	})
	entries := map[string][]byte{
		"oci-layout":    []byte(`{"imageLayoutVersion":"1.0.0"}`),
		"index.json":    index,
		"manifest.json": []byte(`[]`),
	}
	for _, b := range append([][]byte{img.config, img.manifest}, img.layers...) {
		entries["blobs/sha256/"+strings.TrimPrefix(digestOf(b), "sha256:")] = b
	}
	if img.edit != nil {
		img.edit(entries)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, dir := range []string{"blobs/", "blobs/sha256/"} {
		if err := tw.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

type setup struct {
	reg     *fakeRegistry
	cfg     Config
	agents  []string
	logPath string
}

func writeLogin(t *testing.T, path, host, user, password string) {
	t.Helper()
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	if err := os.WriteFile(path, []byte(`{"auths":{"`+host+`":{"auth":"`+auth+`"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newSetup(t *testing.T) *setup {
	t.Helper()
	reg := newFakeRegistry(t)
	dir := t.TempDir()
	uat, demos := filepath.Join(dir, "fitt-ftcdv-uat.json"), filepath.Join(dir, "fitt-demos.json")
	writeLogin(t, uat, reg.host(), testUser, testPassword)
	writeLogin(t, demos, reg.host(), demosUser, demosPassword)
	s := &setup{reg: reg, logPath: filepath.Join(dir, "push.log")}
	s.cfg = Config{Registry: reg.host(), Scheme: "http", Logins: map[string]string{"fitt/ftcdv-uat": uat, "fitt/demos": demos}, Log: s.logPath, WorkDir: dir}
	return s
}

func (s *setup) pushResult(t *testing.T, ref string, layout io.Reader) (Result, error) {
	t.Helper()
	return Push(context.Background(), s.cfg, ref, layout, Deps{AgentSessions: func() ([]string, error) { return s.agents, nil }, Caller: "macagents"})
}

func (s *setup) push(t *testing.T, ref string, layout io.Reader) (string, error) {
	t.Helper()
	res, err := s.pushResult(t, ref, layout)
	return res.Digest, err
}

func (s *setup) ref(project, image, tag string) string {
	return s.reg.host() + "/" + project + "/" + image + ":" + tag
}

func noSecret(t *testing.T, where string) {
	t.Helper()
	if strings.Contains(where, testPassword) || strings.Contains(where, base64.StdEncoding.EncodeToString([]byte(testUser+":"+testPassword))) {
		t.Errorf("the login is in: %s", where)
	}
}

func TestItPushesTheImageItWasGivenAndSaysTheRegistrysDigest(t *testing.T) {
	s := newSetup(t)
	ref := s.ref("fitt/ftcdv-uat", "crn-demo-ntc-film-job-efa0ff04", "v5")
	img := newImage(ref)
	digest, err := s.push(t, ref, img.tar(t))
	if err != nil {
		t.Fatal(err)
	}
	if digest != digestOf(img.manifest) {
		t.Errorf("digest %s, want the manifest's own %s", digest, digestOf(img.manifest))
	}
	got := s.reg.tags["fitt/ftcdv-uat/crn-demo-ntc-film-job-efa0ff04"]["v5"]
	if !bytes.Equal(got, img.manifest) {
		t.Errorf("the registry holds another manifest: %s", got)
	}
	for _, b := range append([][]byte{img.config}, img.layers...) {
		if _, ok := s.reg.blobs[digestOf(b)]; !ok {
			t.Errorf("blob %s was not pushed", digestOf(b))
		}
	}
	log, _ := os.ReadFile(s.logPath)
	if !strings.Contains(string(log), digest) || !strings.Contains(string(log), "pushed") || !strings.Contains(string(log), "macagents") {
		t.Errorf("the log does not record the push: %s", log)
	}
	noSecret(t, string(log))
}

// The one thing an agent could do with the helper is add tags: it never replaces one.
// An app built from its own Dockerfile is tagged with the commit it was built from
// (crn-image.json, FITT Billing 13 Nov): the full 40-hex SHA, never written twice either.
func TestItPushesACommitTag(t *testing.T) {
	s := newSetup(t)
	sha := "78614b84d799ff164ab43d906e81c9c80eeaedbf"
	ref := s.ref("fitt/ftcdv-uat", "crn-demo-fitt-billing-digital-79475c78", sha)
	img := newImage(ref)
	if _, err := s.push(t, ref, img.tar(t)); err != nil {
		t.Fatal(err)
	}
	if got := s.reg.tags["fitt/ftcdv-uat/crn-demo-fitt-billing-digital-79475c78"][sha]; !bytes.Equal(got, img.manifest) {
		t.Errorf("the registry holds another manifest under %s: %s", sha, got)
	}
}

func TestItNeverWritesATagThatIsAlreadyThere(t *testing.T) {
	s := newSetup(t)
	ref := s.ref("fitt/ftcdv-uat", "crn-demo-hello-fitt-c0241b87", "v2")
	s.reg.tags["fitt/ftcdv-uat/crn-demo-hello-fitt-c0241b87"] = map[string][]byte{"v2": []byte("the customer's v2")}
	_, err := s.push(t, ref, newImage(ref).tar(t))
	if err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("err = %v, want: the tag is already there", err)
	}
	if w := s.reg.wrote(); len(w) > 0 {
		t.Errorf("it wrote to the registry: %v", w)
	}
	if string(s.reg.tags["fitt/ftcdv-uat/crn-demo-hello-fitt-c0241b87"]["v2"]) != "the customer's v2" {
		t.Error("the customer's v2 changed")
	}
}

func TestItServesOnlyItsOwnProjectsImagesAndTags(t *testing.T) {
	s := newSetup(t)
	cases := map[string]string{
		"another project":      s.ref("fitt/cust-other", "crn-demo-x-12345678", "v1"),
		"another registry":     "registry.example.test/fitt/ftcdv-uat/crn-demo-x-12345678:v1",
		"not a CRN image":      s.ref("fitt/ftcdv-uat", "base", "v1"),
		"latest":               s.ref("fitt/ftcdv-uat", "crn-demo-x-12345678", "latest"),
		"no tag":               s.reg.host() + "/fitt/ftcdv-uat/crn-demo-x-12345678",
		"a digest":             s.ref("fitt/ftcdv-uat", "crn-demo-x-12345678@sha256", strings.Repeat("a", 64)),
		"a path inside":        s.ref("fitt/ftcdv-uat", "crn-demo-x/../../demos/crn-demo-y", "v1"),
		"a nested image":       s.ref("fitt/ftcdv-uat", "crn-demo-x/inner", "v1"),
		"v0":                   s.ref("fitt/ftcdv-uat", "crn-demo-x-12345678", "v0"),
		"an option":            "--insecure",
		"upper case":           s.ref("fitt/ftcdv-uat", "crn-demo-X-12345678", "v1"),
		"a project like ours":  s.ref("fitt/ftcdv-uat-evil", "crn-demo-x-12345678", "v1"),
		"a group of a project": s.ref("fitt/ftcdv-uat/sub", "crn-demo-x-12345678", "v1"),
		"a short commit":       s.ref("fitt/ftcdv-uat", "crn-demo-x-12345678", "78614b8"),
		"an upper-case commit": s.ref("fitt/ftcdv-uat", "crn-demo-x-12345678", "78614B84D799FF164AB43D906E81C9C80EEAEDBF"),
		"a commit and more":    s.ref("fitt/ftcdv-uat", "crn-demo-x-12345678", "78614b84d799ff164ab43d906e81c9c80eeaedbf0"),
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.push(t, ref, newImage(ref).tar(t)); err == nil {
				t.Fatalf("pushed %s", ref)
			}
			if len(s.reg.requests) > 0 {
				t.Errorf("it asked the registry: %v", s.reg.requests)
			}
		})
	}
}

// CRN pushes only when its build has stopped every agent and holds every build slot.
func TestItRefusesWhileAnAgentIsRunning(t *testing.T) {
	s := newSetup(t)
	s.agents = []string{"4242 claude --resume x"}
	ref := s.ref("fitt/ftcdv-uat", "crn-demo-ntc-film-job-efa0ff04", "v5")
	_, err := s.push(t, ref, newImage(ref).tar(t))
	if err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("err = %v, want a refusal for the agent", err)
	}
	if len(s.reg.requests) > 0 {
		t.Errorf("it asked the registry: %v", s.reg.requests)
	}
}

func TestItRefusesWhenItCannotTellWhetherAnAgentRuns(t *testing.T) {
	s := newSetup(t)
	ref := s.ref("fitt/ftcdv-uat", "crn-demo-ntc-film-job-efa0ff04", "v5")
	_, err := Push(context.Background(), s.cfg, ref, newImage(ref).tar(t), Deps{AgentSessions: func() ([]string, error) { return nil, fmt.Errorf("pgrep: no answer") }})
	if err == nil {
		t.Fatal("it pushed without knowing")
	}
	if len(s.reg.requests) > 0 {
		t.Errorf("it asked the registry: %v", s.reg.requests)
	}
}

func TestItRefusesALayoutThatIsNotOneWholeImageForThisTag(t *testing.T) {
	ref := func(s *setup) string { return s.ref("fitt/ftcdv-uat", "crn-demo-ntc-film-job-efa0ff04", "v5") }
	cases := map[string]func(img *image, s *setup){
		"a blob that is not its name": func(img *image, s *setup) {
			img.edit = func(e map[string][]byte) {
				e["blobs/sha256/"+strings.TrimPrefix(digestOf(img.layers[0]), "sha256:")] = []byte("something else")
			}
		},
		"a blob of the right size that is not its name": func(img *image, s *setup) {
			img.edit = func(e map[string][]byte) {
				e["blobs/sha256/"+strings.TrimPrefix(digestOf(img.layers[0]), "sha256:")] = []byte("layer ONE")
			}
		},
		"a missing layer": func(img *image, s *setup) {
			img.edit = func(e map[string][]byte) {
				delete(e, "blobs/sha256/"+strings.TrimPrefix(digestOf(img.layers[1]), "sha256:"))
			}
		},
		"another image's name": func(img *image, s *setup) {
			img.ref = s.ref("fitt/ftcdv-uat", "crn-demo-hello-fitt-c0241b87", "v5")
		},
		"a link": func(img *image, s *setup) {
			img.edit = func(e map[string][]byte) { e["../escape"] = []byte("x") }
		},
		"an unknown file": func(img *image, s *setup) {
			img.edit = func(e map[string][]byte) { e["blobs/sha512/abc"] = []byte("x") }
		},
		"two images": func(img *image, s *setup) {
			img.edit = func(e map[string][]byte) {
				var idx map[string]any
				_ = json.Unmarshal(e["index.json"], &idx)
				ms := idx["manifests"].([]any)
				idx["manifests"] = append(ms, ms[0])
				e["index.json"], _ = json.Marshal(idx)
			}
		},
		"an index, not a manifest": func(img *image, s *setup) {
			img.edit = func(e map[string][]byte) {
				var idx map[string]any
				_ = json.Unmarshal(e["index.json"], &idx)
				idx["manifests"].([]any)[0].(map[string]any)["mediaType"] = "application/vnd.oci.image.index.v1+json"
				e["index.json"], _ = json.Marshal(idx)
			}
		},
		"a layer of another size": func(img *image, s *setup) {
			var m manifest
			_ = json.Unmarshal(img.manifest, &m)
			m.Layers[0].Size++
			img.manifest, _ = json.Marshal(m)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSetup(t)
			img := newImage(ref(s))
			change(img, s)
			if _, err := s.push(t, ref(s), img.tar(t)); err == nil {
				t.Fatal("it pushed")
			}
			if w := s.reg.wrote(); len(w) > 0 {
				t.Errorf("it wrote to the registry: %v", w)
			}
		})
	}
}

// The login goes only to the GitLab of the registry it serves, whatever the registry says.
func TestItSendsTheLoginToNoOtherHost(t *testing.T) {
	s := newSetup(t)
	other := newFakeRegistry(t)
	u, _ := url.Parse(other.srv.URL)
	s.reg.realm = "http://localhost:" + u.Port() + "/jwt/auth"
	ref := s.ref("fitt/ftcdv-uat", "crn-demo-ntc-film-job-efa0ff04", "v5")
	_, err := s.push(t, ref, newImage(ref).tar(t))
	if err == nil {
		t.Fatal("it pushed")
	}
	if len(other.basic) > 0 || len(other.requests) > 0 {
		t.Errorf("the other host was asked: %v", other.requests)
	}
	noSecret(t, err.Error())
}

func TestTheLoginMustBeItsOwnPrivateFile(t *testing.T) {
	cases := map[string]func(path string){
		"readable by others": func(path string) { _ = os.Chmod(path, 0o644) },
		"a link": func(path string) {
			real := path + ".real"
			_ = os.Rename(path, real)
			_ = os.Symlink(real, path)
		},
		"missing": func(path string) { _ = os.Remove(path) },
		"another registry": func(path string) {
			_ = os.WriteFile(path, []byte(`{"auths":{"registry.example.test":{"auth":"`+base64.StdEncoding.EncodeToString([]byte(testUser+":"+testPassword))+`"}}}`), 0o600)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSetup(t)
			change(s.cfg.Logins["fitt/ftcdv-uat"])
			ref := s.ref("fitt/ftcdv-uat", "crn-demo-ntc-film-job-efa0ff04", "v5")
			_, err := s.push(t, ref, newImage(ref).tar(t))
			if err == nil {
				t.Fatal("it pushed")
			}
			noSecret(t, err.Error())
			if w := s.reg.wrote(); len(w) > 0 {
				t.Errorf("it wrote to the registry: %v", w)
			}
		})
	}
}

func TestAFailedPushIsLoggedWithoutTheLogin(t *testing.T) {
	s := newSetup(t)
	ref := s.ref("fitt/ftcdv-uat", "crn-demo-hello-fitt-c0241b87", "v2")
	s.reg.tags["fitt/ftcdv-uat/crn-demo-hello-fitt-c0241b87"] = map[string][]byte{"v2": []byte("x")}
	if _, err := s.push(t, ref, newImage(ref).tar(t)); err == nil {
		t.Fatal("it pushed")
	}
	log, _ := os.ReadFile(s.logPath)
	if !strings.Contains(string(log), "refused") || !strings.Contains(string(log), ref) {
		t.Errorf("the log does not record the refusal: %s", log)
	}
	noSecret(t, string(log))
}

// Each project's own login, and only for that project: a token is never sent for another.
func TestItUsesTheLoginOfTheImagesProjectAndSaysWhichOne(t *testing.T) {
	s := newSetup(t)
	ref := s.ref("fitt/demos", "crn-demo-digital-value-okr-4ddc9371", "v1")
	res, err := s.pushResult(t, ref, newImage(ref).tar(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Login != demosUser {
		t.Errorf("login %q, want %q", res.Login, demosUser)
	}
	for _, b := range s.reg.basic {
		if !strings.HasPrefix(b, demosUser+":") {
			t.Errorf("another login was sent for fitt/demos: %s", strings.SplitN(b, ":", 2)[0])
		}
	}
	log, _ := os.ReadFile(s.logPath)
	if !strings.Contains(string(log), demosUser) {
		t.Errorf("the log does not say which login pushed: %s", log)
	}
	noSecret(t, string(log))
	if strings.Contains(string(log), demosPassword) {
		t.Errorf("the demos token is in the log: %s", log)
	}
}

func TestItRefusesAProjectItHoldsNoLoginFor(t *testing.T) {
	s := newSetup(t)
	delete(s.cfg.Logins, "fitt/ftcdv-uat")
	ref := s.ref("fitt/ftcdv-uat", "crn-demo-audit-planning-task-management-160001c3", "v4")
	_, err := s.push(t, ref, newImage(ref).tar(t))
	if err == nil || !strings.Contains(err.Error(), "not one this helper serves") {
		t.Fatalf("err = %v, want a refusal for the project", err)
	}
	if len(s.reg.requests) > 0 {
		t.Errorf("it asked the registry: %v", s.reg.requests)
	}
}

func TestLoadConfigReadsOnlyAFileNoOneElseCanWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crn-push.json")
	body := `{"registry":"172.168.1.234:5050","scheme":"http","logins":{"fitt/demos":"/var/crnpush/logins/fitt-demos.json"},"log":"/var/crnpush/push.log","work_dir":"/var/crnpush/tmp"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Registry != "172.168.1.234:5050" || cfg.Logins["fitt/demos"] != "/var/crnpush/logins/fitt-demos.json" || len(cfg.Logins) != 1 || cfg.WorkDir != "/var/crnpush/tmp" {
		t.Errorf("config %+v", cfg)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path, os.Getuid()); err == nil {
		t.Error("it read a config anyone can write")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path, os.Getuid()+1); err == nil {
		t.Error("it read a config owned by someone else")
	}
}

func TestLoadConfigRefusesALoginThatIsNotOneProjectsFile(t *testing.T) {
	for name, logins := range map[string]string{
		"none":             `{}`,
		"a group":          `{"fitt":"/var/crnpush/logins/fitt.json"}`,
		"a relative file":  `{"fitt/demos":"logins/fitt-demos.json"}`,
		"a path in a name": `{"fitt/../demos":"/var/crnpush/logins/x.json"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "crn-push.json")
			body := `{"registry":"172.168.1.234:5050","scheme":"http","logins":` + logins + `,"log":"/var/crnpush/push.log","work_dir":"/var/crnpush/tmp"}`
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path, os.Getuid()); err == nil {
				t.Errorf("it took %s", logins)
			}
		})
	}
}
