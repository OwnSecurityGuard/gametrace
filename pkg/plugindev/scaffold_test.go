package plugindev_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gametrace/pkg/plugindev"
)

// TestScaffoldPluginReturnsContentsOnly locks in the core invariant of the
// external-plugin model: ScaffoldPlugin renders the skeleton and hands the
// content back to the caller — it never writes to disk, because the platform
// does not hold user plugin sources. The agent writes these files into its own
// workspace.
func TestScaffoldPluginReturnsContentsOnly(t *testing.T) {
	res, err := plugindev.ScaffoldPlugin("smokeplugin", "sample", "v1", []string{"length-prefixed"})
	if err != nil {
		t.Fatalf("ScaffoldPlugin returned error: %v", err)
	}
	wantFiles := []string{"go.mod", "main.go", "plugin.yaml"}
	if len(res.Files) != len(wantFiles) {
		t.Fatalf("files=%v want %v", res.Files, wantFiles)
	}
	for i, f := range wantFiles {
		if res.Files[i] != f {
			t.Fatalf("files[%d]=%q want %q (order must be stable)", i, res.Files[i], f)
		}
		if strings.TrimSpace(res.Contents[f]) == "" {
			t.Fatalf("contents[%q] is empty", f)
		}
	}
	if res.Template == "" {
		t.Fatal("template name must be reported so the user knows the file origin")
	}
	if res.SDKVersion == "" {
		t.Fatal("sdk_version must be reported")
	}
	if containsHardcodedPath(res.Contents["go.mod"]) {
		t.Fatalf("generated go.mod must not contain a local path:\n%s", res.Contents["go.mod"])
	}
	if !contains(res.Contents["main.go"], "sdk.RunRegisterLoop") {
		t.Fatalf("generated main.go missing RunRegisterLoop entrypoint:\n%s", res.Contents["main.go"])
	}
}

// TestScaffoldPluginValidatesInput verifies the required parameters are enforced
// locally (the platform never forwards a request it can reject itself).
func TestScaffoldPluginValidatesInput(t *testing.T) {
	if _, err := plugindev.ScaffoldPlugin("", "sample", "", nil); err == nil {
		t.Fatal("expected an error for a missing name")
	}
	if _, err := plugindev.ScaffoldPlugin("foo", "", "", nil); err == nil {
		t.Fatal("expected an error for a missing protocol")
	}
}

// TestScaffoldSmokeBuild verifies that the rendered skeleton actually compiles
// once written into a workspace, and that its go.mod depends only on the
// published SDK module (no baked-in local paths).
//
// The build step is best-effort: it is skipped when the repository SDK
// submodule (./sdk) is unavailable (e.g. a partial checkout) so the unit
// suite stays hermetic.
func TestScaffoldSmokeBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scaffold smoke build in -short mode")
	}

	name := "smokeplugin"
	res, err := plugindev.ScaffoldPlugin(name, "sample", "", nil)
	if err != nil {
		t.Fatalf("ScaffoldPlugin returned error: %v", err)
	}

	// The caller (agent) owns the workspace: write the returned content there.
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(res.Contents[f]), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	// Best-effort build: wire a local replace to the sibling SDK repo, tidy
	// (so the SDK's transitive requirements land in go.mod), then compile.
	// Skipped when the toolchain can't resolve deps (CI without the sibling
	// repo or an offline module cache) so the unit suite stays hermetic.
	sdk := findLocalSDK(t)
	if sdk == "" {
		t.Skip("repository SDK submodule ./sdk not found; skipping build")
	}
	edit := exec.Command("go", "mod", "edit",
		"-replace", "github.com/OwnSecurityGuard/gametrace/sdk="+sdk)
	edit.Dir = dir
	if out, e := edit.CombinedOutput(); e != nil {
		t.Fatalf("go mod edit -replace: %v\n%s", e, out)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, e := tidy.CombinedOutput(); e != nil {
		t.Skipf("go mod tidy failed (deps unavailable?): %v\n%s", e, out)
	}
	build := exec.Command("go", "build", "-o", name+".exe", ".")
	build.Dir = dir
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("go build failed: %v\n%s", e, out)
	}
}

// repoRoot returns the gametrace repository root (two levels up from this package
// directory, pkg/plugindev).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}
	return root
}

// findLocalSDK returns the SDK submodule inside this repository (./sdk), which
// is the single local source of truth now that the SDK has been folded into the
// monorepo. Returns "" when it is absent (e.g. a partial checkout).
//
// The earlier implementation preferred the retired standalone repo
// E:\ai_workspace\gt-plugin-sdk. That checkout still lingers on developer
// machines, and replacing onto it rewrites the SDK module to its old
// identity (module path gt-plugin-sdk, old proto types), so the scaffolded
// main.go no longer type-checks against sdk.DecodeFuncV2. It also made the
// suite fail locally while silently skipping in CI — never look outside ./sdk.
func findLocalSDK(t *testing.T) string {
	t.Helper()
	sdk := filepath.Join(repoRoot(t), "sdk")
	if _, err := os.Stat(sdk); err != nil {
		return ""
	}
	return sdk
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

// containsHardcodedPath reports whether s bakes in a filesystem path (e.g. a
// replace directive or an absolute/relative module path), which the published
// scaffold template must never do. A plain `require` line with a module path
// is fine; only a `=>` rewrite or a `replace` pointing at a path trips this.
func containsHardcodedPath(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "=>") {
			return true
		}
		if strings.HasPrefix(trimmed, "replace") && (strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\")) {
			return true
		}
	}
	return false
}