package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	classfile "github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/build/cache"
	"github.com/goplus/llar/internal/formula/repo"
	"github.com/goplus/llar/internal/modules"
	"github.com/goplus/llar/internal/vcs"
	"github.com/goplus/llar/mod/module"
)

// mod creates a Module with the given path, version, and direct deps.
func mod(path, version string, deps ...*modules.Module) *modules.Module {
	return &modules.Module{
		Path:    path,
		Version: version,
		Deps:    deps,
	}
}

// paths returns the "Path@Version" strings for []*modules.Module.
func paths(mods []*modules.Module) string {
	var s []string
	for _, m := range mods {
		s = append(s, fmt.Sprintf("%s@%s", m.Path, m.Version))
	}
	return strings.Join(s, " ")
}

// versions returns the "Path@Version" strings for []module.Version.
func versions(vers []module.Version) string {
	var s []string
	for _, v := range vers {
		s = append(s, fmt.Sprintf("%s@%s", v.Path, v.Version))
	}
	return strings.Join(s, " ")
}

func TestConstructBuildList(t *testing.T) {
	b := &Builder{}

	t.Run("single module", func(t *testing.T) {
		A := mod("A", "1.0.0")
		got := b.constructBuildList([]*modules.Module{A})
		if want := "A@1.0.0"; paths(got) != want {
			t.Errorf("got %q, want %q", paths(got), want)
		}
	})

	t.Run("linear chain", func(t *testing.T) {
		// A -> B -> C
		C := mod("C", "1.0.0")
		B := mod("B", "1.0.0", C)
		A := mod("A", "1.0.0", B, C) // main has all deps
		got := b.constructBuildList([]*modules.Module{A, B, C})
		if want := "C@1.0.0 B@1.0.0 A@1.0.0"; paths(got) != want {
			t.Errorf("got %q, want %q", paths(got), want)
		}
	})

	t.Run("diamond", func(t *testing.T) {
		// A -> B -> C, A -> D -> C
		C := mod("C", "1.2.0")
		B := mod("B", "1.2.0", C)
		D := mod("D", "1.0.0", C)
		A := mod("A", "1.0.0", B, C, D) // main has all deps
		got := b.constructBuildList([]*modules.Module{A, B, C, D})
		// C first (leaf), then B, then D, then A (root)
		if want := "C@1.2.0 B@1.2.0 D@1.0.0 A@1.0.0"; paths(got) != want {
			t.Errorf("got %q, want %q", paths(got), want)
		}
	})

	t.Run("deep chain", func(t *testing.T) {
		// A -> B -> C -> D -> E
		E := mod("E", "1.0.0")
		D := mod("D", "1.0.0", E)
		C := mod("C", "1.0.0", D)
		B := mod("B", "1.0.0", C)
		A := mod("A", "1.0.0", B, C, D, E)
		got := b.constructBuildList([]*modules.Module{A, B, C, D, E})
		if want := "E@1.0.0 D@1.0.0 C@1.0.0 B@1.0.0 A@1.0.0"; paths(got) != want {
			t.Errorf("got %q, want %q", paths(got), want)
		}
	})

	t.Run("empty", func(t *testing.T) {
		got := b.constructBuildList(nil)
		if len(got) != 0 {
			t.Errorf("got %d modules, want 0", len(got))
		}
	})
}

func TestResolveModTransitiveDeps(t *testing.T) {
	b := &Builder{}

	t.Run("case1: simple", func(t *testing.T) {
		// C -> D
		D := mod("D", "1.0.0")
		C := mod("C", "1.2.0", D)
		B := mod("B", "1.2.0")
		A := mod("A", "1.0.0", B, C, D)
		targets := []*modules.Module{A, B, C, D}

		got := b.resolveModTransitiveDeps(targets, C)
		if want := "D@1.0.0"; versions(got) != want {
			t.Errorf("got %q, want %q", versions(got), want)
		}
	})

	t.Run("case2: diamond", func(t *testing.T) {
		// A -> B -> C, A -> D -> C  (MVS selects C@2.0.0)
		C := mod("C", "2.0.0")
		B := mod("B", "1.2.0", C)
		D := mod("D", "1.0.0", C)
		A := mod("A", "1.0.0", B, C, D)
		targets := []*modules.Module{A, B, C, D}

		got := b.resolveModTransitiveDeps(targets, B)
		if want := "C@2.0.0"; versions(got) != want {
			t.Errorf("got %q, want %q", versions(got), want)
		}
	})

	t.Run("case3: diamond with transitive dep", func(t *testing.T) {
		// A -> B -> C, A -> D -> C -> E  (MVS selects C@2.0.0)
		E := mod("E", "1.0.0")
		C := mod("C", "2.0.0", E)
		B := mod("B", "1.2.0", C)
		D := mod("D", "1.0.0", C)
		A := mod("A", "1.0.0", B, C, D, E)
		targets := []*modules.Module{A, B, C, D, E}

		got := b.resolveModTransitiveDeps(targets, B)
		if want := "E@1.0.0 C@2.0.0"; versions(got) != want {
			t.Errorf("got %q, want %q", versions(got), want)
		}
	})

	t.Run("case4: multiple direct deps", func(t *testing.T) {
		// B -> C, B -> D  (C and D are independent leaves)
		C := mod("C", "1.1.0")
		D := mod("D", "1.0.0")
		B := mod("B", "1.2.0", C, D)
		A := mod("A", "1.0.0", B, C, D)
		targets := []*modules.Module{A, B, C, D}

		got := b.resolveModTransitiveDeps(targets, B)
		if want := "C@1.1.0 D@1.0.0"; versions(got) != want {
			t.Errorf("got %q, want %q", versions(got), want)
		}
	})

	t.Run("case5: dep ordering by topology", func(t *testing.T) {
		// B -> C -> D, B -> D
		D := mod("D", "1.2.0")
		C := mod("C", "1.1.0", D)
		B := mod("B", "1.2.0", C, D)
		A := mod("A", "1.0.0", B, C, D)
		targets := []*modules.Module{A, B, C, D}

		got := b.resolveModTransitiveDeps(targets, B)
		// D before C because C depends on D
		if want := "D@1.2.0 C@1.1.0"; versions(got) != want {
			t.Errorf("got %q, want %q", versions(got), want)
		}
	})

	t.Run("leaf module has no deps", func(t *testing.T) {
		D := mod("D", "1.0.0")
		A := mod("A", "1.0.0", D)
		targets := []*modules.Module{A, D}

		got := b.resolveModTransitiveDeps(targets, D)
		if len(got) != 0 {
			t.Errorf("got %q, want empty", versions(got))
		}
	})

	t.Run("deep transitive chain", func(t *testing.T) {
		// A -> B -> C -> D -> E
		E := mod("E", "1.0.0")
		D := mod("D", "1.0.0", E)
		C := mod("C", "1.0.0", D)
		B := mod("B", "1.0.0", C)
		A := mod("A", "1.0.0", B, C, D, E)
		targets := []*modules.Module{A, B, C, D, E}

		got := b.resolveModTransitiveDeps(targets, B)
		if want := "E@1.0.0 D@1.0.0 C@1.0.0"; versions(got) != want {
			t.Errorf("got %q, want %q", versions(got), want)
		}
	})

	t.Run("shared transitive dep", func(t *testing.T) {
		// A -> B -> D, A -> C -> D
		D := mod("D", "2.0.0")
		B := mod("B", "1.0.0", D)
		C := mod("C", "1.0.0", D)
		A := mod("A", "1.0.0", B, C, D)
		targets := []*modules.Module{A, B, C, D}

		// resolve for A: B and C both need D
		got := b.resolveModTransitiveDeps(targets, A)
		// D first (shared leaf), then B, then C
		if want := "D@2.0.0 B@1.0.0 C@1.0.0"; versions(got) != want {
			t.Errorf("got %q, want %q", versions(got), want)
		}
	})

	t.Run("w-shaped cross dependencies", func(t *testing.T) {
		// B -> C -> F, B -> C -> G
		// B -> D -> F, B -> D -> G
		F := mod("F", "1.0.0")
		G := mod("G", "1.0.0")
		C := mod("C", "1.0.0", F, G)
		D := mod("D", "1.0.0", F, G)
		B := mod("B", "1.0.0", C, D)
		A := mod("A", "1.0.0", B, C, D, F, G)
		targets := []*modules.Module{A, B, C, D, F, G}

		got := b.resolveModTransitiveDeps(targets, B)
		// F and G are leaves, then C and D (both depend on F,G), order follows DFS
		if want := "F@1.0.0 G@1.0.0 C@1.0.0 D@1.0.0"; versions(got) != want {
			t.Errorf("got %q, want %q", versions(got), want)
		}
	})

	t.Run("circular dependency", func(t *testing.T) {
		// B -> C -> D -> B (cycle)
		// visited breaks the cycle at B
		D := mod("D", "1.0.0")
		C := mod("C", "1.0.0", D)
		B := mod("B", "1.0.0", C)
		D.Deps = []*modules.Module{B} // close the cycle: D -> B
		A := mod("A", "1.0.0", B, C, D)
		targets := []*modules.Module{A, B, C, D}

		got := b.resolveModTransitiveDeps(targets, B)
		// B is excluded (mod itself), D -> B is a back-edge (B already visited)
		// so: visit(C) -> visit(D) -> visit(B) no-op -> append D -> append C
		if want := "D@1.0.0 C@1.0.0"; versions(got) != want {
			t.Errorf("got %q, want %q", versions(got), want)
		}
	})

	t.Run("wide fan-out", func(t *testing.T) {
		// B -> C, B -> D, B -> E  (all leaves, no inter-deps)
		C := mod("C", "1.0.0")
		D := mod("D", "1.0.0")
		E := mod("E", "1.0.0")
		B := mod("B", "1.0.0", C, D, E)
		A := mod("A", "1.0.0", B, C, D, E)
		targets := []*modules.Module{A, B, C, D, E}

		got := b.resolveModTransitiveDeps(targets, B)
		if want := "C@1.0.0 D@1.0.0 E@1.0.0"; versions(got) != want {
			t.Errorf("got %q, want %q", versions(got), want)
		}
	})
}

// testFormulaDir and testSourceDir are resolved once at init to avoid
// issues with os.Chdir in Build() changing the working directory.
var (
	testFormulaDir string
	testSourceDir  string
)

func init() {
	testFormulaDir, _ = filepath.Abs("testdata/formulas")
	testSourceDir, _ = filepath.Abs("testdata/sources")
}

// ---------------------------------------------------------------------------
// Test helpers for Build() tests
// ---------------------------------------------------------------------------

// setupTestStore copies testdata/formulas to a temp dir and returns a Store.
// The mock VCS Sync is a no-op since data is already in place.
func setupTestStore(t *testing.T) repo.Store {
	t.Helper()
	storeDir := t.TempDir()
	if err := os.CopyFS(storeDir, os.DirFS(testFormulaDir)); err != nil {
		t.Fatalf("failed to copy testdata: %v", err)
	}
	return repo.New(storeDir, newMockRepo(storeDir))
}

// setupBuilder creates a Builder wired with a test Store and mock source repos.
func setupBuilder(t *testing.T, store repo.Store, matrix string) *Builder {
	t.Helper()
	workspaceDir := t.TempDir()
	return &Builder{
		store:        store,
		matrix:       matrix,
		workspaceDir: workspaceDir,
		cache:        &localCache{workspaceDir: workspaceDir},
		newRepo: func(repoPath string) (vcs.Repo, error) {
			modPath := strings.TrimPrefix(repoPath, "github.com/")
			return newMockRepo(filepath.Join(testSourceDir, modPath)), nil
		},
	}
}

// loadAndBuild loads modules via modules.Load then builds them.
func loadAndBuild(t *testing.T, b *Builder, store repo.Store, main module.Version) ([]Result, []*modules.Module) {
	t.Helper()
	ctx := context.Background()
	mods, err := modules.Load(ctx, main, modules.Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("modules.Load(%s) failed: %v", main.Path, err)
	}
	results, err := b.Build(ctx, mods)
	if err != nil {
		t.Fatalf("Build(%s) failed: %v", main.Path, err)
	}
	return results, mods
}

// findResult returns the Result for a given module path.
// Results are in constructBuildList order, so we match via build order.
func findResult(results []Result, b *Builder, mods []*modules.Module, path string) (Result, bool) {
	buildOrder := b.constructBuildList(mods)
	for i, m := range buildOrder {
		if m.Path == path && i < len(results) {
			return results[i], true
		}
	}
	return Result{}, false
}

type cachePut struct {
	key    cache.Key
	output fs.FS
	entry  cache.Entry
}

type recordingCache struct {
	hits map[module.Version]cache.Entry
	puts []cachePut
}

func (c *recordingCache) Get(ctx context.Context, key cache.Key) (cache.Entry, bool, error) {
	if c.hits == nil {
		return cache.Entry{}, false, nil
	}
	entry, ok := c.hits[key.Module]
	return entry, ok, nil
}

func (c *recordingCache) Put(ctx context.Context, key cache.Key, output fs.FS, entry cache.Entry) (cache.Entry, error) {
	c.puts = append(c.puts, cachePut{
		key:    key,
		output: output,
		entry:  entry,
	})
	return entry, nil
}

type graphLockStore struct {
	held     map[string]bool
	events   []string
	failPath string
	failErr  error
}

func (s *graphLockStore) ModuleFS(context.Context, string) (fs.FS, error) {
	return nil, errors.New("unexpected ModuleFS")
}

func (s *graphLockStore) LockModule(path string) (func(), error) {
	s.events = append(s.events, "lock "+path)
	if path == s.failPath {
		return nil, s.failErr
	}
	if s.held == nil {
		s.held = make(map[string]bool)
	}
	if s.held[path] {
		return nil, fmt.Errorf("module %s is already locked", path)
	}
	s.held[path] = true
	return func() {
		delete(s.held, path)
		s.events = append(s.events, "unlock "+path)
	}, nil
}

type graphLockCache struct {
	store *graphLockStore
	paths []string
	gets  int
}

func (c *graphLockCache) Get(_ context.Context, _ cache.Key) (cache.Entry, bool, error) {
	for _, path := range c.paths {
		if !c.store.held[path] {
			return cache.Entry{}, false, fmt.Errorf("module %s is not locked", path)
		}
	}
	c.gets++
	return cache.Entry{Metadata: "cached"}, true, nil
}

func (*graphLockCache) Put(context.Context, cache.Key, fs.FS, cache.Entry) (cache.Entry, error) {
	return cache.Entry{}, errors.New("unexpected cache Put")
}

type oppositeGraphLocks struct {
	mu           sync.Mutex
	locks        map[string]chan struct{}
	attempts     [2]string
	attempted    chan struct{}
	acquired     int
	bothAcquired chan struct{}
}

func newOppositeGraphLocks() *oppositeGraphLocks {
	return &oppositeGraphLocks{
		locks:        make(map[string]chan struct{}),
		attempted:    make(chan struct{}),
		bothAcquired: make(chan struct{}),
	}
}

func (l *oppositeGraphLocks) lock(path string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	lock := l.locks[path]
	if lock == nil {
		lock = make(chan struct{}, 1)
		lock <- struct{}{}
		l.locks[path] = lock
	}
	return lock
}

func (l *oppositeGraphLocks) recordAttempt(id int, path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[id] = path
	if l.attempts[0] != "" && l.attempts[1] != "" {
		close(l.attempted)
	}
}

func (l *oppositeGraphLocks) attemptsDiffer() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.attempts[0] != l.attempts[1]
}

func (l *oppositeGraphLocks) recordAcquired() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.acquired++
	if l.acquired == 2 {
		close(l.bothAcquired)
	}
}

type oppositeGraphStore struct {
	id    int
	locks *oppositeGraphLocks
	seen  bool
}

func (*oppositeGraphStore) ModuleFS(context.Context, string) (fs.FS, error) {
	return nil, errors.New("unexpected ModuleFS")
}

func (s *oppositeGraphStore) LockModule(path string) (func(), error) {
	firstShared := !s.seen && (path == "test/x" || path == "test/y")
	if firstShared {
		s.seen = true
		s.locks.recordAttempt(s.id, path)
		<-s.locks.attempted
	}

	lock := s.locks.lock(path)
	<-lock
	if firstShared && s.locks.attemptsDiffer() {
		s.locks.recordAcquired()
		<-s.locks.bothAcquired
	}
	return func() { lock <- struct{}{} }, nil
}

// ---------------------------------------------------------------------------
// NewBuilder tests
// ---------------------------------------------------------------------------

func TestNewBuilder(t *testing.T) {
	t.Run("with workspace dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		store := setupTestStore(t)
		b, err := NewBuilder(Options{
			Store:        store,
			MatrixStr:    "amd64-linux",
			WorkspaceDir: tmpDir,
		})
		if err != nil {
			t.Fatalf("NewBuilder() error = %v", err)
		}
		if b.workspaceDir != tmpDir {
			t.Errorf("workspaceDir = %q, want %q", b.workspaceDir, tmpDir)
		}
		if b.matrix != "amd64-linux" {
			t.Errorf("matrix = %q, want %q", b.matrix, "amd64-linux")
		}
		if b.store != store {
			t.Error("store not set correctly")
		}
		if b.newRepo == nil {
			t.Error("newRepo should be set to default")
		}
	})

	t.Run("default workspace dir", func(t *testing.T) {
		b, err := NewBuilder(Options{
			MatrixStr: "arm64-darwin",
		})
		if err != nil {
			t.Fatalf("NewBuilder() error = %v", err)
		}
		if b.workspaceDir == "" {
			t.Error("workspaceDir should not be empty")
		}
		// Verify the default workspace directory was created
		if _, err := os.Stat(b.workspaceDir); err != nil {
			t.Errorf("default workspace dir not created: %v", err)
		}
		if !strings.Contains(b.workspaceDir, ".llar") {
			t.Errorf("workspace dir %q doesn't contain .llar", b.workspaceDir)
		}
	})
}

// ---------------------------------------------------------------------------
// Build error path tests
// ---------------------------------------------------------------------------

func TestBuild_EmptyTargets(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")

	results, err := b.Build(context.Background(), nil)
	if err != nil {
		t.Fatalf("Build(nil) error = %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestBuild_LocksEntireDependencyGraph(t *testing.T) {
	depA := mod("a/dep", "1.0.0")
	depZ := mod("z/dep", "1.0.0")
	root := mod("m/root", "1.0.0", depZ, depA)
	paths := []string{"a/dep", "m/root", "z/dep"}
	store := &graphLockStore{}
	buildCache := &graphLockCache{store: store, paths: paths}
	b := &Builder{
		store:        store,
		matrix:       "amd64-linux",
		workspaceDir: t.TempDir(),
		cache:        buildCache,
	}

	results, err := b.Build(context.Background(), []*modules.Module{root, depZ, depA})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || buildCache.gets != 3 {
		t.Fatalf("Build returned %d results and %d cache gets, want 3 each", len(results), buildCache.gets)
	}
	wantEvents := []string{
		"lock a/dep", "lock m/root", "lock z/dep",
		"unlock z/dep", "unlock m/root", "unlock a/dep",
	}
	if !slices.Equal(store.events, wantEvents) {
		t.Fatalf("lock events = %q, want %q", store.events, wantEvents)
	}
	if len(store.held) != 0 {
		t.Fatalf("locks remain held after Build: %v", store.held)
	}
}

func TestBuild_ReleasesGraphLocksAfterLockError(t *testing.T) {
	dep := mod("a/dep", "1.0.0")
	root := mod("m/root", "1.0.0", dep)
	wantErr := errors.New("lock failed")
	store := &graphLockStore{failPath: "m/root", failErr: wantErr}
	buildCache := &graphLockCache{store: store, paths: []string{"a/dep", "m/root"}}
	b := &Builder{
		store:        store,
		matrix:       "amd64-linux",
		workspaceDir: t.TempDir(),
		cache:        buildCache,
	}

	if _, err := b.Build(context.Background(), []*modules.Module{root, dep}); !errors.Is(err, wantErr) {
		t.Fatalf("Build error = %v, want %v", err, wantErr)
	}
	wantEvents := []string{"lock a/dep", "lock m/root", "unlock a/dep"}
	if !slices.Equal(store.events, wantEvents) {
		t.Fatalf("lock events = %q, want %q", store.events, wantEvents)
	}
	if len(store.held) != 0 {
		t.Fatalf("locks remain held after failure: %v", store.held)
	}
	if buildCache.gets != 0 {
		t.Fatalf("cache Get calls = %d, want 0", buildCache.gets)
	}
}

func TestBuild_OppositeGraphOrdersDoNotDeadlock(t *testing.T) {
	x1 := mod("test/x", "1.0.0")
	y1 := mod("test/y", "1.0.0")
	x1.Deps = []*modules.Module{y1}
	root1 := mod("test/root1", "1.0.0", x1)

	x2 := mod("test/x", "1.0.0")
	y2 := mod("test/y", "1.0.0")
	y2.Deps = []*modules.Module{x2}
	root2 := mod("test/root2", "1.0.0", y2)

	locks := newOppositeGraphLocks()
	newBuilder := func(id int) *Builder {
		return &Builder{
			store:        &oppositeGraphStore{id: id, locks: locks},
			matrix:       "amd64-linux",
			workspaceDir: t.TempDir(),
			cache: &recordingCache{hits: map[module.Version]cache.Entry{
				{Path: "test/x", Version: "1.0.0"}:     {Metadata: "x"},
				{Path: "test/y", Version: "1.0.0"}:     {Metadata: "y"},
				{Path: "test/root1", Version: "1.0.0"}: {Metadata: "root1"},
				{Path: "test/root2", Version: "1.0.0"}: {Metadata: "root2"},
			}},
		}
	}
	builder1 := newBuilder(0)
	builder2 := newBuilder(1)

	start := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		<-start
		_, err := builder1.Build(context.Background(), []*modules.Module{root1, x1, y1})
		done <- err
	}()
	go func() {
		<-start
		_, err := builder2.Build(context.Background(), []*modules.Module{root2, y2, x2})
		done <- err
	}()
	close(start)

	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Build deadlocked while acquiring opposite graph orders")
		}
	}
}

func TestBuild_RepoCreationError(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")

	wantErr := errors.New("repo creation failed")
	b.newRepo = func(repoPath string) (vcs.Repo, error) {
		return nil, wantErr
	}

	main := module.Version{Path: "test/liba", Version: "1.0.0"}
	ctx := context.Background()
	mods, err := modules.Load(ctx, main, modules.Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("modules.Load() failed: %v", err)
	}
	_, err = b.Build(ctx, mods)
	if err == nil {
		t.Fatal("Build() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "repo creation failed") {
		t.Errorf("error = %v, want it to contain %q", err, "repo creation failed")
	}
}

func TestBuild_SyncError(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")

	wantErr := errors.New("sync failed")
	b.newRepo = func(repoPath string) (vcs.Repo, error) {
		return &errorRepo{syncErr: wantErr}, nil
	}

	main := module.Version{Path: "test/liba", Version: "1.0.0"}
	ctx := context.Background()
	mods, err := modules.Load(ctx, main, modules.Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("modules.Load() failed: %v", err)
	}
	_, err = b.Build(ctx, mods)
	if err == nil {
		t.Fatal("Build() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "sync failed") {
		t.Errorf("error = %v, want it to contain %q", err, "sync failed")
	}
}

// ---------------------------------------------------------------------------
// Build cache detail tests (not covered by e2e)
// ---------------------------------------------------------------------------

func TestBuild_PrePopulatedCache(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")

	// Pre-populate cache with a different metadata value
	cache := &buildCache{}
	cache.set("1.0.0", "amd64-linux", &buildEntry{
		Metadata:  "-lPRECACHED",
		BuildTime: time.Now(),
	})
	if err := b.saveCache("test/liba", cache); err != nil {
		t.Fatalf("saveCache() failed: %v", err)
	}

	main := module.Version{Path: "test/liba", Version: "1.0.0"}
	results, _ := loadAndBuild(t, b, store, main)

	// Should return pre-cached metadata, not the formula-defined "-lA"
	if results[0].Metadata != "-lPRECACHED" {
		t.Errorf("metadata = %q, want %q (from pre-populated cache)", results[0].Metadata, "-lPRECACHED")
	}
}

func TestBuild_CacheWrittenCorrectly(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")

	main := module.Version{Path: "test/liba", Version: "1.0.0"}
	loadAndBuild(t, b, store, main)

	// Load the cache file and verify its content
	cache, err := b.loadCache("test/liba")
	if err != nil {
		t.Fatalf("loadCache() failed: %v", err)
	}
	entry, ok := cache.get("1.0.0", "amd64-linux")
	if !ok {
		t.Fatal("cache entry not found for 1.0.0-amd64-linux")
	}
	if entry.Metadata != "-lA" {
		t.Errorf("cached metadata = %q, want %q", entry.Metadata, "-lA")
	}
	if entry.BuildTime.IsZero() {
		t.Error("cache build time should not be zero")
	}

	// Verify it's valid JSON on disk
	cacheDir, _ := b.cacheDir("test/liba")
	data, err := os.ReadFile(filepath.Join(cacheDir, cacheFile))
	if err != nil {
		t.Fatalf("ReadFile() failed: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("cache file is not valid JSON: %v", err)
	}
}

func TestBuild_CacheAccumulatesMultipleVersions(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")

	main := module.Version{Path: "test/liba", Version: "1.0.0"}
	loadAndBuild(t, b, store, main)

	// Manually add another version to the cache
	cache, _ := b.loadCache("test/liba")
	cache.set("2.0.0", "amd64-linux", &buildEntry{
		Metadata:  "-lA2",
		BuildTime: time.Now(),
	})
	b.saveCache("test/liba", cache)

	// Build again - should still hit cache for 1.0.0
	results, _ := loadAndBuild(t, b, store, main)
	if results[0].Metadata != "-lA" {
		t.Errorf("metadata = %q, want %q", results[0].Metadata, "-lA")
	}

	// Verify both entries exist
	cache, _ = b.loadCache("test/liba")
	if _, ok := cache.get("1.0.0", "amd64-linux"); !ok {
		t.Error("cache miss for 1.0.0")
	}
	if _, ok := cache.get("2.0.0", "amd64-linux"); !ok {
		t.Error("cache miss for 2.0.0")
	}
}

func TestBuild_CustomCacheReceivesBuiltArtifacts(t *testing.T) {
	store := setupTestStore(t)
	c := &recordingCache{}
	b := setupBuilder(t, store, "amd64-linux")
	b.cache = c

	main := module.Version{Path: "test/depresult", Version: "1.0.0"}
	results, mods := loadAndBuild(t, b, store, main)

	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	if len(c.puts) != 2 {
		t.Fatalf("cache puts len = %d, want 2", len(c.puts))
	}
	liba := module.Version{Path: "test/liba", Version: "1.0.0"}
	if c.puts[0].key.Module != liba {
		t.Fatalf("cache puts[0] module = %+v, want %+v", c.puts[0].key.Module, liba)
	}
	if c.puts[0].entry.Metadata != "-lA" {
		t.Fatalf("cache puts[0] metadata = %q, want -lA", c.puts[0].entry.Metadata)
	}
	if c.puts[0].entry.Deps != nil {
		t.Fatalf("cache puts[0] deps = %+v, want nil", c.puts[0].entry.Deps)
	}
	if _, err := fs.Stat(c.puts[0].output, "."); err != nil {
		t.Fatalf("cache puts[0] output is not readable: %v", err)
	}
	depresult := module.Version{Path: "test/depresult", Version: "1.0.0"}
	if c.puts[1].key.Module != depresult {
		t.Fatalf("cache puts[1] module = %+v, want %+v", c.puts[1].key.Module, depresult)
	}
	wantDeps := []module.Version{liba}
	if !slices.Equal(c.puts[1].entry.Deps, wantDeps) {
		t.Fatalf("cache puts[1] deps = %+v, want %+v", c.puts[1].entry.Deps, wantDeps)
	}
	if _, ok := findResult(results, b, mods, "test/depresult"); !ok {
		t.Fatal("missing result for test/depresult")
	}
}

func TestBuild_CustomCacheHitSkipsOnBuild(t *testing.T) {
	store := setupTestStore(t)
	c := &recordingCache{
		hits: map[module.Version]cache.Entry{
			{Path: "test/liba", Version: "1.0.0"}: {Metadata: "-lCUSTOM"},
		},
	}
	b := setupBuilder(t, store, "amd64-linux")
	b.cache = c

	main := module.Version{Path: "test/liba", Version: "1.0.0"}
	results, _ := loadAndBuild(t, b, store, main)

	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	if results[0].Metadata != "-lCUSTOM" {
		t.Fatalf("metadata = %q, want -lCUSTOM", results[0].Metadata)
	}
	if len(c.puts) != 0 {
		t.Fatalf("cache puts len = %d, want 0", len(c.puts))
	}
}

// ---------------------------------------------------------------------------
// OnTest (RunTest) behaviour tests
// ---------------------------------------------------------------------------

// TestBuild_RunTest_DisabledSkipsOnTest verifies that OnTest callbacks are not
// invoked when Builder.runTest is false, even if the formula defines one.
func TestBuild_RunTest_DisabledSkipsOnTest(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")
	// runTest is false by default.

	main := module.Version{Path: "test/liba", Version: "1.0.0"}
	ctx := context.Background()
	mods, err := modules.Load(ctx, main, modules.Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("modules.Load() failed: %v", err)
	}

	// Inject an OnTest callback that would fail the build if invoked.
	var called bool
	for _, m := range mods {
		m.OnTest = func(ctx *classfile.Context, proj *classfile.Project, out *classfile.TestResult) {
			called = true
			out.AddErr(errors.New("onTest should not have been invoked"))
		}
	}

	if _, err := b.Build(ctx, mods); err != nil {
		t.Fatalf("Build() error = %v, want nil", err)
	}
	if called {
		t.Error("OnTest was invoked despite runTest=false")
	}
}

// TestBuild_RunTest_EnabledSurfacesOnTestError verifies that when runTest is
// enabled, OnTest runs after OnBuild and its errors are wrapped with context
// identifying the failing module.
func TestBuild_RunTest_EnabledSurfacesOnTestError(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")
	b.runTest = true

	main := module.Version{Path: "test/liba", Version: "1.0.0"}
	ctx := context.Background()
	mods, err := modules.Load(ctx, main, modules.Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("modules.Load() failed: %v", err)
	}

	wantErr := errors.New("boom from onTest")
	for _, m := range mods {
		m.OnTest = func(ctx *classfile.Context, proj *classfile.Project, out *classfile.TestResult) {
			out.AddErr(wantErr)
		}
	}

	_, err = b.Build(ctx, mods)
	if err == nil {
		t.Fatal("Build() error = nil, want onTest failure")
	}
	if !strings.Contains(err.Error(), "onTest failed for test/liba@1.0.0") {
		t.Errorf("error = %v, want it to describe the failing module", err)
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error chain does not wrap injected cause: %v", err)
	}
}

// TestBuild_RunTest_ReusesCacheWhenHit verifies that runTest=true reuses the
// build cache: on a cache hit, OnBuild is skipped (cached metadata is returned
// as-is) and OnTest still runs against the cached artifacts. The cache entry
// must not be rewritten since nothing was rebuilt.
func TestBuild_RunTest_ReusesCacheWhenHit(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")
	b.runTest = true

	// Pre-populate cache with a sentinel metadata value. If the cache is
	// consulted and reused (as the new behavior requires), Build() will
	// return this value without re-running OnBuild.
	cache := &buildCache{}
	cache.set("1.0.0", "amd64-linux", &buildEntry{
		Metadata:  "-lCACHED",
		BuildTime: time.Now(),
	})
	if err := b.saveCache("test/liba", cache); err != nil {
		t.Fatalf("saveCache() failed: %v", err)
	}

	main := module.Version{Path: "test/liba", Version: "1.0.0"}
	ctx := context.Background()
	mods, err := modules.Load(ctx, main, modules.Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("modules.Load() failed: %v", err)
	}

	var testCalled bool
	for _, m := range mods {
		m.OnTest = func(ctx *classfile.Context, proj *classfile.Project, out *classfile.TestResult) {
			testCalled = true
		}
	}

	results, err := b.Build(ctx, mods)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// OnTest must have run even though OnBuild was skipped via cache hit.
	if !testCalled {
		t.Error("OnTest was not invoked despite runTest=true on cache hit")
	}
	// Result metadata should be the cached sentinel value, not the fresh
	// "-lA" that OnBuild would have produced.
	if len(results) == 0 || results[len(results)-1].Metadata != "-lCACHED" {
		t.Errorf("metadata = %+v, want last entry %q (cache should be reused)", results, "-lCACHED")
	}

	// Cache entry must remain the same sentinel (nothing new to save).
	cacheAfter, err := b.loadCache("test/liba")
	if err != nil {
		t.Fatalf("loadCache() failed: %v", err)
	}
	entry, ok := cacheAfter.get("1.0.0", "amd64-linux")
	if !ok {
		t.Fatal("cache entry removed; expected pre-populated entry to remain")
	}
	if entry.Metadata != "-lCACHED" {
		t.Errorf("cache metadata = %q after cache-hit test run, want %q (no rewrite expected)", entry.Metadata, "-lCACHED")
	}
}

// TestBuild_RunTest_SavesCacheOnMiss verifies that runTest=true writes the
// build cache on a cache miss: OnBuild runs, fresh metadata is produced,
// and the result is persisted for later invocations to reuse.
func TestBuild_RunTest_SavesCacheOnMiss(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")
	b.runTest = true

	// No pre-populated cache: this is a cache miss.

	main := module.Version{Path: "test/liba", Version: "1.0.0"}
	ctx := context.Background()
	mods, err := modules.Load(ctx, main, modules.Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("modules.Load() failed: %v", err)
	}

	var testCalled bool
	for _, m := range mods {
		m.OnTest = func(ctx *classfile.Context, proj *classfile.Project, out *classfile.TestResult) {
			testCalled = true
		}
	}

	results, err := b.Build(ctx, mods)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if !testCalled {
		t.Error("OnTest was not invoked despite runTest=true")
	}
	// OnBuild ran; result metadata should reflect the formula output.
	if len(results) == 0 || results[len(results)-1].Metadata != "-lA" {
		t.Errorf("metadata = %+v, want last entry %q (OnBuild should run on cache miss)", results, "-lA")
	}

	// Cache must have been written with the freshly-built metadata so the
	// next invocation can reuse it.
	cacheAfter, err := b.loadCache("test/liba")
	if err != nil {
		t.Fatalf("loadCache() failed: %v", err)
	}
	entry, ok := cacheAfter.get("1.0.0", "amd64-linux")
	if !ok {
		t.Fatal("cache entry missing; expected cache write after cache-miss test run")
	}
	if entry.Metadata != "-lA" {
		t.Errorf("cache metadata = %q, want %q (cache should be written)", entry.Metadata, "-lA")
	}
}

// TestBuild_RunTest_DepOnTestNotInvoked verifies that when runTest is
// enabled, OnTest is invoked only on the root target. A dependency that
// happens to define OnTest must NOT see it triggered by a test run whose
// target is a downstream consumer. This matches the product design
// (issues/106 §9): "onTest runs only after the main module build".
func TestBuild_RunTest_DepOnTestNotInvoked(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")
	b.runTest = true

	// test/depresult depends on test/liba.
	main := module.Version{Path: "test/depresult", Version: "1.0.0"}
	ctx := context.Background()
	mods, err := modules.Load(ctx, main, modules.Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("modules.Load() failed: %v", err)
	}

	var rootCalled, depCalled bool
	for _, m := range mods {
		m := m
		switch m.Path {
		case "test/depresult":
			m.OnTest = func(_ *classfile.Context, _ *classfile.Project, _ *classfile.TestResult) {
				rootCalled = true
			}
		case "test/liba":
			m.OnTest = func(_ *classfile.Context, _ *classfile.Project, out *classfile.TestResult) {
				depCalled = true
				out.AddErr(errors.New("dep OnTest should not have been invoked"))
			}
		}
	}

	if _, err := b.Build(ctx, mods); err != nil {
		t.Fatalf("Build() error = %v, want nil (dep OnTest must not run)", err)
	}
	if !rootCalled {
		t.Error("root OnTest was not invoked")
	}
	if depCalled {
		t.Error("dep OnTest was invoked; runTest must only test the root target")
	}
}

// TestBuild_RunTest_DepCacheStillUsed verifies that when runTest is enabled,
// only the root target bypasses the build cache. Dependencies whose entries
// exist in the cache must still short-circuit through cache lookup so test
// runs do not pay the cost of rebuilding the full dependency tree.
func TestBuild_RunTest_DepCacheStillUsed(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")
	b.runTest = true

	// Pre-populate liba's cache with a sentinel metadata; if liba is rebuilt
	// it would produce "-lA" instead, so the sentinel is a unique cache signal.
	cache := &buildCache{}
	cache.set("1.0.0", "amd64-linux", &buildEntry{
		Metadata:  "-lA-CACHED",
		BuildTime: time.Now(),
	})
	if err := b.saveCache("test/liba", cache); err != nil {
		t.Fatalf("saveCache() failed: %v", err)
	}

	main := module.Version{Path: "test/depresult", Version: "1.0.0"}
	ctx := context.Background()
	mods, err := modules.Load(ctx, main, modules.Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("modules.Load() failed: %v", err)
	}

	results, err := b.Build(ctx, mods)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	libaResult, ok := findResult(results, b, mods, "test/liba")
	if !ok {
		t.Fatal("missing result for test/liba")
	}
	if libaResult.Metadata != "-lA-CACHED" {
		t.Errorf("liba metadata = %q, want %q (dep cache should be consulted under runTest)", libaResult.Metadata, "-lA-CACHED")
	}
}

// ---------------------------------------------------------------------------
// Environment and workspace tests (not covered by e2e)
// ---------------------------------------------------------------------------

func TestBuild_EnvRestoration(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")

	const envKey = "LLAR_BUILD_TEST_ENV"
	os.Setenv(envKey, "original")
	defer os.Unsetenv(envKey)

	main := module.Version{Path: "test/liba", Version: "1.0.0"}
	loadAndBuild(t, b, store, main)

	if got := os.Getenv(envKey); got != "original" {
		t.Errorf("env %s = %q after Build, want %q (restored)", envKey, got, "original")
	}
}

func TestBuild_EnvRestoration_AfterError(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")

	const envKey = "LLAR_BUILD_TEST_ENV_ERR"
	os.Setenv(envKey, "before_error")
	defer os.Unsetenv(envKey)

	main := module.Version{Path: "test/errmod", Version: "1.0.0"}
	ctx := context.Background()
	mods, err := modules.Load(ctx, main, modules.Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("modules.Load() failed: %v", err)
	}
	_, _ = b.Build(ctx, mods)

	// Environment should still be restored even after error
	if got := os.Getenv(envKey); got != "before_error" {
		t.Errorf("env %s = %q after failed Build, want %q", envKey, got, "before_error")
	}
}

func TestBuild_InstallDirConvention(t *testing.T) {
	store := setupTestStore(t)
	b := setupBuilder(t, store, "amd64-linux")

	main := module.Version{Path: "test/liba", Version: "1.0.0"}
	loadAndBuild(t, b, store, main)

	installDir, _ := b.installDir("test/liba", "1.0.0")

	// Verify the path follows workspace/<escaped>@<version>-<matrix>
	rel, err := filepath.Rel(b.workspaceDir, installDir)
	if err != nil {
		t.Fatalf("installDir not under workspace: %v", err)
	}
	want := filepath.Join("test", "liba@1.0.0-amd64-linux")
	if rel != want {
		t.Errorf("installDir rel = %q, want %q", rel, want)
	}

	// Verify directory was created
	if _, err := os.Stat(installDir); err != nil {
		t.Errorf("installDir not created: %v", err)
	}
}

// ---------------------------------------------------------------------------
// constructBuildList additional tests
// ---------------------------------------------------------------------------

func TestConstructBuildList_DuplicatePaths(t *testing.T) {
	b := &Builder{}

	// Modules with same path at different versions (MVS should have resolved this,
	// but constructBuildList should handle it gracefully)
	C := mod("C", "1.0.0")
	B := mod("B", "1.0.0", C)
	A := mod("A", "1.0.0", B, C)
	got := b.constructBuildList([]*modules.Module{A, B, C})

	// Each path should appear exactly once
	seen := make(map[string]bool)
	for _, m := range got {
		if seen[m.Path] {
			t.Errorf("duplicate path in build list: %s", m.Path)
		}
		seen[m.Path] = true
	}
	if len(got) != 3 {
		t.Errorf("got %d modules, want 3", len(got))
	}
}

func TestConstructBuildList_DepNotInTargets(t *testing.T) {
	b := &Builder{}

	// B depends on C, but C is not in targets
	C := mod("C", "1.0.0")
	B := mod("B", "1.0.0", C)
	A := mod("A", "1.0.0", B)
	got := b.constructBuildList([]*modules.Module{A, B})
	// C should be skipped since it's not in targets
	if want := "B@1.0.0 A@1.0.0"; paths(got) != want {
		t.Errorf("got %q, want %q", paths(got), want)
	}
}

// ---------------------------------------------------------------------------
// resolveModTransitiveDeps additional tests
// ---------------------------------------------------------------------------

func TestResolveModTransitiveDeps_ModNotInTargets(t *testing.T) {
	b := &Builder{}

	// mod's deps reference a module not in targets
	D := mod("D", "1.0.0")
	C := mod("C", "1.0.0", D)
	B := mod("B", "1.0.0", C)
	A := mod("A", "1.0.0", B, C)
	targets := []*modules.Module{A, B, C} // D is NOT in targets

	got := b.resolveModTransitiveDeps(targets, B)
	// C is reachable, D is not in targets so skipped
	if want := "C@1.0.0"; versions(got) != want {
		t.Errorf("got %q, want %q", versions(got), want)
	}
}

// ---------------------------------------------------------------------------
// Mock types for error testing
// ---------------------------------------------------------------------------

// errorRepo implements vcs.Repo and returns configurable errors.
type errorRepo struct {
	syncErr error
}

func (e *errorRepo) Tags(ctx context.Context) ([]string, error) {
	return []string{"v1.0.0"}, nil
}

func (e *errorRepo) Latest(ctx context.Context) (string, error) {
	return "abc123", nil
}

func (e *errorRepo) At(ref, localDir string) fs.FS {
	return os.DirFS(".")
}

func (e *errorRepo) Sync(ctx context.Context, ref, path, destDir string) error {
	return e.syncErr
}
