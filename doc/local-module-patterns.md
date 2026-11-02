Local Module Patterns for `llar make`
====

## Overview

`llar make` supports local filesystem patterns to build formulas from the local
working directory instead of fetching them from the remote llarhub repository.
This is primarily used in CI within the llarhub repo itself, where formulas
under development need to be built locally while their dependencies are still
resolved from remote.

## Supported Patterns

| Pattern | Meaning | Version |
|---------|---------|---------|
| `owner/repo@ver` | Remote module | Specified by `@ver` |
| `owner/repo` | Remote module | Resolved to latest via git tags |
| `.` | Current directory module | Resolved to latest via git tags |
| `..` | Parent directory module (subject to local root boundary) | Resolved to latest via git tags |
| `./@ver` | Current directory module | Specified by `@ver` |
| `../repo@ver` | Relative local module path | Specified by `@ver` |
| `./owner/repo` | Module at specified path | Resolved to latest via git tags |
| `./owner/repo@ver` | Module at specified path | Specified by `@ver` |
| `/abs/path/to/module@ver` | Absolute local module path (subject to local root boundary) | Specified by `@ver` |

Local patterns follow Go-style filesystem forms (`.`, `..`, `./x`, `../x`,
absolute paths). `.` is shorthand for current directory.
The syntax `.@version` is **invalid** and produces an error — use `./@version`
instead, which aligns with `./owner/repo@version`.

For safety, local patterns are still constrained by the nearest local module
root (`versions.json`): paths escaping that root are rejected.

When version is omitted, `modules.Load` resolves the latest version from the
module's source repository git tags using the formula-defined comparator.

### Planned (TODO)

| Pattern | Meaning | Version |
|---------|---------|---------|
| `./owner/...` | All modules under an owner | Cannot specify; each resolved to latest |
| `./...` | All modules in current tree | Cannot specify; each resolved to latest |

These wildcard patterns are not yet supported because there is no way to
specify per-module versions.

## Architecture

Three orthogonal components compose to support this feature:

```
cmd/llar/internal/make.go          (CLI argument parsing)
         |
         |  parseModuleArg(arg) -> (pattern, version, isLocal, err)
         |
         v
modules/modlocal/                  (local module discovery)
         |
         |  Resolve(cwd, pattern) -> []Module{Path, Dir}
         |
         v
formula/repo/overlay.go            (store composition)
         |
         |  NewOverlayStore(remote, locals) -> Store
         |
         v
modules.Load(ctx, main, Options{FormulaStore: store})
```

Each component has a single responsibility and communicates through simple
data (`map[string]string`) or interfaces (`repo.Store`):

- **`parseModuleArg`** — Detects local filesystem patterns (`.`, `..`, `./`,
  `../`, absolute paths), separates `@version`, validates syntax
  (rejects `.@version`), returns whether the argument
  targets local modules. CLI-specific logic, lives in `cmd/llar/internal/`.

- **`modlocal.Resolve`** — Scans the filesystem for `versions.json` files
  matching the given pattern, reads their `path` field, and returns
  `(Path, Dir)` pairs. No dependency on `modules` or `repo` packages.
  Lives in `modules/modlocal/`.

- **`repo.NewOverlayStore`** — Decorates a remote `Store` with local
  directory overrides. Modules in the locals map are served via `os.DirFS`;
  all others delegate to the remote store. Implements `repo.Store` interface.
  Lives in `formula/repo/`.

`modules.Load` is unchanged — it receives a `Store` and does not know whether
modules come from remote or local sources.

## Pattern Resolution Details

### `.` / `..` / relative / absolute local paths

Walks **up** from `cwd` looking for `versions.json`. Reads its `path` field
to determine the module path. For non-empty local patterns, resolves a target
directory from `(cwd, pattern)` (absolute patterns stay absolute; relative
patterns are joined to `cwd`) and reads `targetDir/versions.json`. The `path` field determines
the module path (which may differ from the directory structure).

## Store Interface

`repo.Store` was extracted from the concrete `Store` struct to an interface:

```go
type Store interface {
    ModuleFS(ctx context.Context, modPath string) (fs.FS, error)
    LockModule(modPath string) (unlock func(), err error)
}
```

The original implementation is now `remoteStore` (unexported). Both
`remoteStore` and `overlayStore` implement `Store`. This change is
transparent to all existing callers.

## Execution Flow

```
llar make ./madler/zlib@v1.3.1

1. parseModuleArg("./madler/zlib@v1.3.1")
   -> pattern="madler/zlib", version="v1.3.1", isLocal=true

2. modlocal.Resolve(cwd, "madler/zlib")
   -> reads cwd/madler/zlib/versions.json
   -> [{Path: "madler/zlib", Dir: "/abs/path/madler/zlib"}]

3. repo.NewOverlayStore(remoteStore, {"madler/zlib": "/abs/path/madler/zlib"})
   -> overlayStore that serves madler/zlib from local FS

4. modules.Load(ctx, {Path: "madler/zlib", Version: "v1.3.1"}, opts)
   -> formula loaded from local FS via overlay
   -> dependencies resolved from remote llarhub

5. builder.Build(ctx, mods)
   -> builds in dependency order as usual
```
