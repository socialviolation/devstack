package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func svcYAML(name string, port int) string {
	return `version: 1
service:
  name: ` + name + `
runtime:
  workDir: .
  run:
    command: echo ` + name + `
ports:
  http: ` + itoa(port) + `
`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func oneRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repos", "mono")
	mustWriteFile(t, filepath.Join(root, WorkspaceManifestFileName), `version: 1
workspace:
  name: playground
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/mono
`)
	for name, body := range files {
		mustWriteFile(t, filepath.Join(repo, name), body)
	}
	return root, repo
}

func TestOneDirectoryDeclaresSeveralServices(t *testing.T) {
	root, repo := oneRepo(t, map[string]string{
		"devstack.orbit-api.yaml": svcYAML("orbit-api", 5100),
		"devstack.orbit-web.yaml": svcYAML("orbit-web", 4201),
	})

	rw, err := ResolveWorkspace(root)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if len(rw.Services) != 2 {
		t.Fatalf("resolved %d services, want 2: %+v", len(rw.Services), rw.Services)
	}
	for _, name := range []string{"orbit-api", "orbit-web"} {
		svc, ok := rw.Services[name]
		if !ok {
			t.Fatalf("service %q was not resolved", name)
		}
		if svc.RepoPath != repo {
			t.Errorf("%s RepoPath = %q, want %q", name, svc.RepoPath, repo)
		}
		if want := filepath.Join(repo, "devstack."+name+".yaml"); svc.ManifestPath != want {
			t.Errorf("%s ManifestPath = %q, want %q", name, svc.ManifestPath, want)
		}
	}
}

func TestTheOriginalFileStillResolvesBesideANewOne(t *testing.T) {
	root, repo := oneRepo(t, map[string]string{
		ServiceManifestFileName: svcYAML("api", 8080),
		"devstack.worker.yaml":  svcYAML("worker", 8081),
	})

	rw, err := ResolveWorkspace(root)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if len(rw.Services) != 2 {
		t.Fatalf("resolved %d services, want 2: %+v", len(rw.Services), rw.Services)
	}
	if got := rw.Services["api"].ManifestPath; got != filepath.Join(repo, ServiceManifestFileName) {
		t.Errorf("api ManifestPath = %q, want the original file", got)
	}
}

func TestTheWorkspaceManifestIsNotAServiceManifest(t *testing.T) {
	if IsServiceManifestName(WorkspaceManifestFileName) {
		t.Error("devstack.workspace.yaml must never be read as a service manifest")
	}
	if !IsServiceManifestName(ServiceManifestFileName) {
		t.Error("devstack.service.yaml is a service manifest")
	}
	if !IsServiceManifestName("devstack.orbit-web.yaml") {
		t.Error("devstack.<name>.yaml is a service manifest")
	}
	if IsServiceManifestName("devstack.service.yml") {
		t.Error("only .yaml is a service manifest")
	}
}

func TestTwoFilesNamingOneServiceAreRefused(t *testing.T) {
	root, _ := oneRepo(t, map[string]string{
		"devstack.a.yaml": svcYAML("same", 1000),
		"devstack.b.yaml": svcYAML("same", 1001),
	})

	_, err := ResolveWorkspace(root)
	if err == nil {
		t.Fatal("two files declaring one service name must be refused")
	}
	for _, want := range []string{"devstack.a.yaml", "devstack.b.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %q, got: %v", want, err)
		}
	}
}

// A hook error names the file the reader must open. Where a directory declares
// several services, that file is devstack.<name>.yaml, and the conventional
// name would send the reader to a file that is not there.
func TestAHookErrorNamesTheFileTheServiceCameFrom(t *testing.T) {
	root, _ := oneRepo(t, map[string]string{
		"devstack.orbit-api.yaml": svcYAML("orbit-api", 5100) + `hooks:
  - name: seed
    on: [service.start]
`,
	})

	_, err := ResolveWorkspace(root)
	if err == nil {
		t.Fatal("a hook with no 'run' command must be refused")
	}
	if !strings.Contains(err.Error(), "devstack.orbit-api.yaml: hook") {
		t.Errorf("the error must scope the hook to devstack.orbit-api.yaml, got: %v", err)
	}
	if strings.Contains(err.Error(), ServiceManifestFileName+": hook") {
		t.Errorf("the error must not name a file the directory does not hold, got: %v", err)
	}
}

func TestIdentityIsUnnamedWhereADirectoryHoldsSeveralServices(t *testing.T) {
	_, repo := oneRepo(t, map[string]string{
		"devstack.orbit-api.yaml": svcYAML("orbit-api", 5100),
		"devstack.orbit-web.yaml": svcYAML("orbit-web", 4201),
	})

	id, err := ResolveIdentity(repo)
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if id.ServiceName != "" {
		t.Errorf("ServiceName = %q, want none: the directory holds two services", id.ServiceName)
	}
}

func TestIdentityStillNamesTheOnlyServiceInADirectory(t *testing.T) {
	_, repo := oneRepo(t, map[string]string{ServiceManifestFileName: svcYAML("api", 8080)})

	id, err := ResolveIdentity(repo)
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if id.ServiceName != "api" {
		t.Errorf("ServiceName = %q, want api", id.ServiceName)
	}
}
