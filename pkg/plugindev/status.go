package plugindev

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// fixedSourceFiles are the non-Go files whose modification time is compared
// against the compiled binary to decide staleness. go.mod is included because
// a dependency change also requires a rebuild; plugin.yaml because the
// manifest is validated at registration.
var fixedSourceFiles = []string{"go.mod", "plugin.yaml"}

// listSourceFiles returns every staleness-relevant source file in the plugin
// directory: all *.go files plus fixedSourceFiles. The earlier hard-coded
// {main.go, go.mod, plugin.yaml} missed decode.go / parser.go — editing those
// left binary_stale=false while the running binary was outdated.
func listSourceFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fixedSourceFiles
	}
	files := make([]string, 0, len(entries)+len(fixedSourceFiles))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		files = append(files, e.Name())
	}
	files = append(files, fixedSourceFiles...)
	return files
}

// ArtifactStateOf derives the Developer Plane's view of a plugin's code purely
// from disk: unknown → scaffolded → compiled. The validated promotion (which
// depends on the runtime, see design §2.2) is applied by Status using the
// Tracker's proof.
func ArtifactStateOf(root, name string) *ArtifactState {
	dir := filepath.Join(root, name)
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return &ArtifactState{State: "unknown"}
	}
	state := &ArtifactState{State: "scaffolded", SourceDir: dir}

	// A binary present means at least one successful build happened.
	binary := filepath.Join(dir, name+exeExt())
	if binInfo, binErr := os.Stat(binary); binErr == nil {
		state.BinaryPath = binary
		binaryStale := false
		binMod := binInfo.ModTime()
		// Compare against the newest source file; a newer source means the
		// binary is stale and must be rebuilt.
		for _, sf := range listSourceFiles(dir) {
			if si, se := os.Stat(filepath.Join(dir, sf)); se == nil {
				if si.ModTime().After(binMod) {
					binaryStale = true
					break
				}
			}
		}
		state.BinaryStale = binaryStale
		state.State = "compiled"
	}
	return state
}

// Status returns the Developer Plane portion of the dual-state view: the
// artifact (disk), the dev-launched process (if any), and the last attempt.
// The registry-derived runtime state is filled in by the MCP layer, which
// talks to the Runtime Plane.
func Status(ctx context.Context, req *StatusRequest) (*PluginStatus, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	artifact := ArtifactStateOf(req.Root, req.Name)

	// Promoted to validated only when a cross-plane proof exists and the
	// binary is not stale (design §2.2 invalidation rule). The proof lives in
	// the process-local Tracker AND on disk (方案 B，pipeline verify 写盘)：
	// docker 部署下两平面不同进程，pipeline 进程内 Tracker 的 proof 对
	// Developer Plane 不可见，必须先查内存、再落盘兜底。
	if artifact.State == "compiled" && !artifact.BinaryStale {
		if p := defaultTracker.ValidatedProof(req.Name); p != nil {
			artifact.State = "validated"
		} else if p := LoadValidation(req.Root, req.Name); p != nil {
			artifact.State = "validated"
		}
	}

	ps := &PluginStatus{
		Name:        req.Name,
		Artifact:    artifact,
		LastAttempt: defaultTracker.LastAttempt(req.Name),
	}

	if lp := defaultTracker.proc(req.Name); lp != nil {
		ps.DevProcess = &DevProcess{
			Launched:   true,
			PID:        lp.pid,
			InstanceID: lp.instanceID,
			Alive:      lp.alive(),
			LaunchedAt: lp.launchedAt,
		}
	}
	return ps, nil
}

// devProcessView is a small helper used by the server to surface the dev
// process in the gRPC response without exposing the mutex internals.
func devProcessView(name string) *DevProcess {
	lp := defaultTracker.proc(name)
	if lp == nil {
		return nil
	}
	return &DevProcess{
		Launched:   true,
		PID:        lp.pid,
		InstanceID: lp.instanceID,
		Alive:      lp.alive(),
		LaunchedAt: lp.launchedAt,
	}
}
