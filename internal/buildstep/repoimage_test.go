package buildstep

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FITT Billing (13 Nov) is Express + Vite with its own deploy/billing.Dockerfile: no root
// package.json, so the Next.js release check can never pass on it. Its owner commits
// crn-image.json, and CRN builds the commit with that Dockerfile instead.

const billingSHA = "78614b84d799ff164ab43d906e81c9c80eeaedbf"

const billingImageJSON = `{
  "schema_version": 1,
  "dockerfile": "deploy/billing.Dockerfile",
  "revision_build_arg": "BILLING_REVISION",
  "user": "node"
}
`

func billingRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(RepoImageFile, billingImageJSON)
	write("deploy/billing.Dockerfile", "FROM node:22\nARG BILLING_REVISION\nLABEL org.opencontainers.image.revision=$BILLING_REVISION\n")
	write("deploy/billing.Dockerfile.dockerignore", "node_modules\n")
	write("server/index.ts", "export {}\n")
	return dir
}

func TestARepoWithoutCrnImageJSONIsNotBuiltFromItsOwnDockerfile(t *testing.T) {
	img, err := ReadRepoImage(t.TempDir())
	if err != nil || img != nil {
		t.Fatalf("ReadRepoImage = %+v, %v; want nil, nil", img, err)
	}
}

func TestCrnImageJSONNamesTheDockerfileTheBuildArgAndTheUser(t *testing.T) {
	img, err := ReadRepoImage(billingRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	want := RepoImage{SchemaVersion: 1, Dockerfile: "deploy/billing.Dockerfile", RevisionBuildArg: "BILLING_REVISION", User: "node"}
	if *img != want {
		t.Errorf("read %+v, want %+v", *img, want)
	}
	if args := img.BuildArgs(billingSHA); len(args) != 1 || args["BILLING_REVISION"] != billingSHA {
		t.Errorf("build args = %v, want only BILLING_REVISION=%s", args, billingSHA)
	}
}

func TestCrnImageJSONMustBeExact(t *testing.T) {
	cases := map[string]string{
		"an unknown field":     `{"schema_version":1,"dockerfile":"deploy/billing.Dockerfile","platform":"linux/arm64"}`,
		"another version":      `{"schema_version":2,"dockerfile":"deploy/billing.Dockerfile"}`,
		"no dockerfile":        `{"schema_version":1}`,
		"a path out":           `{"schema_version":1,"dockerfile":"../billing.Dockerfile"}`,
		"an absolute path":     `{"schema_version":1,"dockerfile":"/etc/billing.Dockerfile"}`,
		"an unclean path":      `{"schema_version":1,"dockerfile":"deploy//billing.Dockerfile"}`,
		"a missing file":       `{"schema_version":1,"dockerfile":"deploy/other.Dockerfile"}`,
		"a directory":          `{"schema_version":1,"dockerfile":"deploy"}`,
		"a build arg with a =": `{"schema_version":1,"dockerfile":"deploy/billing.Dockerfile","revision_build_arg":"A=B"}`,
		"a user with a space":  `{"schema_version":1,"dockerfile":"deploy/billing.Dockerfile","user":"node root"}`,
		"two objects":          `{"schema_version":1,"dockerfile":"deploy/billing.Dockerfile"}{}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := billingRepo(t)
			if err := os.WriteFile(filepath.Join(dir, RepoImageFile), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if img, err := ReadRepoImage(dir); err == nil {
				t.Fatalf("accepted %s as %+v", body, img)
			}
		})
	}
}

func TestTheDockerfileMustNotBeALink(t *testing.T) {
	dir := billingRepo(t)
	file := filepath.Join(dir, "deploy/billing.Dockerfile")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", file); err != nil {
		t.Fatal(err)
	}
	if img, err := ReadRepoImage(dir); err == nil {
		t.Fatalf("accepted a linked Dockerfile: %+v", img)
	}
}

// The owner decides how the image is built, not the agent: a build may change none of it.
func TestABuildMayNotChangeHowTheImageIsBuilt(t *testing.T) {
	cases := map[string]func(dir string) error{
		"the Dockerfile": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "deploy/billing.Dockerfile"), []byte("FROM evil\n"), 0o644)
		},
		"its .dockerignore": func(dir string) error {
			return os.Remove(filepath.Join(dir, "deploy/billing.Dockerfile.dockerignore"))
		},
		"a root .dockerignore": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("*\n"), 0o644)
		},
		"crn-image.json": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, RepoImageFile), []byte(strings.Replace(billingImageJSON, `"node"`, `"root"`, 1)), 0o644)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			dir := billingRepo(t)
			base, err := SnapshotRepoImage(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := change(dir); err != nil {
				t.Fatal(err)
			}
			if err := base.Unchanged(dir); err == nil {
				t.Fatalf("a build changed %s and it passed", name)
			}
		})
	}
}

func TestABuildMayChangeEverythingElse(t *testing.T) {
	dir := billingRepo(t)
	base, err := SnapshotRepoImage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte("BILLING_ENV=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := base.Unchanged(dir); err != nil {
		t.Fatal(err)
	}
	if base.Image == nil || base.Image.Dockerfile != "deploy/billing.Dockerfile" {
		t.Errorf("the snapshot holds %+v, want the owner's crn-image.json", base.Image)
	}
}

// Otherwise an agent could move a Next.js app out of the release check by writing one.
func TestABuildMayNotAddCrnImageJSON(t *testing.T) {
	dir := t.TempDir()
	base, err := SnapshotRepoImage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if base.Image != nil {
		t.Fatalf("an empty repo has %+v", base.Image)
	}
	if err := os.WriteFile(filepath.Join(dir, RepoImageFile), []byte(billingImageJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := base.Unchanged(dir); err == nil || !strings.Contains(err.Error(), RepoImageFile) {
		t.Fatalf("err = %v, want a refusal naming %s", err, RepoImageFile)
	}
}

func TestCommitImageTag(t *testing.T) {
	got := CommitImageTag("172.168.1.234:5050/fitt/ftcdv-uat", "FITT Billing - Digital", "79475c78-5b0b-490a-8d6e-650b19d69c5c", billingSHA)
	want := "172.168.1.234:5050/fitt/ftcdv-uat/crn-demo-fitt-billing-digital-79475c78:" + billingSHA
	if got != want {
		t.Errorf("CommitImageTag = %q, want %q", got, want)
	}
}

func TestBuildImageFromPassesTheRepoDockerfileAndBuildArg(t *testing.T) {
	argsFile := stubDocker(t, "exit 0")
	dir := billingRepo(t)
	err := BuildImageFrom(context.Background(), dir, "deploy/billing.Dockerfile", "reg/fitt/x/crn-demo-x:"+billingSHA,
		map[string]string{"BILLING_REVISION": billingSHA}, map[string]string{"fitt.crn.build_no": "5"}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(argsFile)
	got := strings.TrimSpace(string(raw))
	for _, want := range []string{
		"build --platform linux/amd64 --provenance=false --sbom=false",
		"-f " + filepath.Join(dir, "deploy/billing.Dockerfile"),
		"--build-arg BILLING_REVISION=" + billingSHA,
		"--label fitt.crn.build_no=5",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("docker %s\nlacks %q", got, want)
		}
	}
	if !strings.HasSuffix(got, " "+dir) {
		t.Errorf("docker %s\ndoes not build the context %s", got, dir)
	}
}

func inspectJSON(arch, user, revision string) string {
	return `[{"Os":"linux","Architecture":"` + arch + `","Config":{"User":"` + user + `","Labels":{"org.opencontainers.image.revision":"` + revision + `"}}}]`
}

func TestCheckRepoImage(t *testing.T) {
	cases := map[string]struct {
		inspect, want string
	}{
		"as built":         {inspectJSON("amd64", "node", billingSHA), ""},
		"another revision": {inspectJSON("amd64", "node", "449f94d6888d62444177057f1f8b8d23fcdd5ede"), "org.opencontainers.image.revision"},
		"no revision":      {`[{"Os":"linux","Architecture":"amd64","Config":{"User":"node","Labels":null}}]`, "org.opencontainers.image.revision"},
		"arm64":            {inspectJSON("arm64", "node", billingSHA), "linux/amd64"},
		"root":             {inspectJSON("amd64", "", billingSHA), "user"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			stubDocker(t, "cat <<'EOF'\n"+c.inspect+"\nEOF")
			err := CheckRepoImage(context.Background(), "reg/fitt/x/crn-demo-x:"+billingSHA, billingSHA, "node")
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one naming %s", err, c.want)
			}
		})
	}
}
