# LLAR Project Guide

## Design Rules

1. No useless abstraction - don't abstract if not necessary
2. Abstracted modules must have wide usage - only extract when broadly used
3. All modules must be clean - each module should only handle its own responsibility

## Project Conventions

### Command and Flag Changes

- Follow the existing Cobra command style.
- Add command flags next to the command's existing flags in `init()` using `cmd.Flags()`.
- For example, `make` flags belong beside `makeVerbose` and `makeOutput` in `cmd/llar/internal/make.go`.
- Do not hand-roll `os.Args` normalization or custom command parsing when Cobra/pflag already supports the command shape.
- Single-dash flags are shorthands in pflag. Add a shorthand with `BoolVarP`, `StringVarP`, etc. when short syntax is needed; use `--flag` for long flags.
- Keep command flags command-local unless the behavior is genuinely global.
- Write Cobra `Short`/`Long` text and README command summaries in terms of stable, user-facing product responsibilities. Do not define a command by implementation details such as internal services, callback names, cache steps, or internal directories.
- Keep the core command boundaries explicit. For example, `install` obtains and installs builds from LLAR Cloud, `make` builds from source using LLAR formulas, and `test` verifies installed artifacts from a consumer's perspective.

### Command Tests

- Put formula fixtures under the existing `testdata/formulas` tree instead of generating formula files inline in tests.
- Prefer the existing test helpers such as `setupLocalFormulas`, `withMockRemoteStore`, `isolatedWorkspaceDir`, and `prepopulateCache`.
- Tests for `llar make` should exercise the real Cobra command path where possible, then use fixtures and prepopulated cache to avoid network and source builds.
- When tests touch package-level command flag variables, reset or restore those variables so one test cannot affect another.

## Project Overview

LLAR is a multi-language module manager built with XGo (gop) and xgo. It uses classfile mechanism for defining build formulas.

## XGO Classfile Mechanism

### What is Classfile?

Classfile is a DSL (Domain Specific Language) mechanism in xgo that allows defining custom file extensions with specific behavior. Each classfile extension maps to a Go struct that acts as a "class".

### How Classfile Works

1. **Registration**: Classfiles are registered via `xgobuild.RegisterProject()` in `internal/ixgo/classfile.go`

2. **File Extension Mapping**:
   - `_llar.gox` -> `ModuleF` class (formula/classfile.go)
   - `_cmp.gox` -> `CmpApp` class (formula/classfile.go)

3. **Code Generation**: When a `.gox` file is processed:
   - The filename prefix (before `_`) becomes the struct name
   - Example: `hello_llar.gox` generates struct `hello` embedding `ModuleF`
   - A `MainEntry()` method is generated containing the DSL code
   - A `Main()` method calls `Gopt_ModuleF_Main(this)`

### Example

Source file `hello_llar.gox`:
```gox
id "DaveGamble/cJSON"
fromVer "v1.0.0"

onRequire (proj, deps) => {
   echo "hello"
}

onBuild (ctx, proj, out) => {
    echo "hello"
}
```

Generated Go code:
```go
package main

import (
    "fmt"
    "github.com/goplus/llar/formula"
)

type hello struct {
    formula.ModuleF
}

func (this *hello) MainEntry() {
    this.Id("DaveGamble/cJSON")
    this.FromVer("v1.0.0")
    this.OnRequire(func(proj *formula.Project, deps *formula.ModuleDeps) {
        fmt.Println("hello")
    })
    this.OnBuild(func(ctx *formula.Context, proj *formula.Project, out *formula.BuildResult) {
        fmt.Println("hello")
    })
}

func (this *hello) Main() {
    formula.Gopt_ModuleF_Main(this)
}

func main() {
    new(hello).Main()
}
```

### Key Points

1. **Struct Name Derivation**: The struct name comes from `strings.Cut(filename, "_")` - the part before the first underscore

2. **Class Entry Point**: `Gopt_<ClassName>_Main` is the classfile entry point that:
   - Calls `MainEntry()` to execute DSL code
   - Initializes the embedded `gsh.App`

3. **Error Handling**: If a file doesn't match any registered classfile pattern (wrong suffix), `BuildFile` returns "undefined" errors for DSL functions

## Build & Test

```bash
# Run tests with required ldflags (Go 1.24+)
go test -ldflags="-checklinkname=0" ./...

# Run specific package tests with coverage
go test -ldflags="-checklinkname=0" -cover ./internal/formula/...
```

## Project Structure

- `formula/` - Classfile definitions (ModuleF, CmpApp, Context, Project, etc.)
- `internal/formula/` - Formula loading and interpretation logic
- `internal/ixgo/` - ixgo classfile registration
- `mod/` - Module and version handling
