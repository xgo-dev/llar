<!--
<p align="center">
  <img width="250" height="250" alt="LLAR Logo" src=".github/logo.png" />
</p>
-->

# LLAR

[![Test](https://github.com/goplus/llar/actions/workflows/go.yml/badge.svg)](https://github.com/goplus/llar/actions/workflows/go.yml)
[![codecov](https://codecov.io/gh/goplus/llar/branch/main/graph/badge.svg)](https://codecov.io/gh/goplus/llar)
[![GoDoc](https://pkg.go.dev/badge/github.com/xgo-dev/llar.svg)](https://pkg.go.dev/github.com/xgo-dev/llar)
[![XGo](https://img.shields.io/badge/project-XGo-blue.svg)](https://github.com/goplus/xgo)

LLAR is a cloud-based multi-language package manager built with [XGo](https://github.com/goplus/xgo). It resolves dependencies, downloads source code, and builds libraries from source using declarative formulas.

## Installation

```bash
go install -ldflags="-checklinkname=0" github.com/goplus/llar/cmd/llar@latest
```

## Usage

### Install a package

```bash
# Install zlib and its dependencies
llar install madler/zlib@v1.3.2

# Show remote build output
llar install -v madler/zlib@v1.3.2

# Install a selected build matrix
llar install --os linux --arch amd64 --option shared=ON madler/zlib@v1.3.2

# Print the installed module result as JSON
llar install --json madler/zlib@v1.3.2

# Install and copy the root artifact into a directory
llar install -o ./zlib-out madler/zlib@v1.3.2

# Install and export the root artifact as an archive
llar install -o zlib.tar.gz madler/zlib@v1.3.2
```

### Build a package

```bash
# Build zlib
llar make madler/zlib@v1.3.1

# Build with verbose output
llar make -v madler/zlib@v1.3.1

# Print the build result as JSON
llar make --json madler/zlib@v1.3.1

# Build and export to a directory
llar make -o ./output madler/zlib@v1.3.1

# Build and export as a zip archive
llar make -o zlib.zip madler/zlib@v1.3.1

# Build a local formula
llar make ./@1.0.0
llar make ./madler/zlib@v1.3.1
```

### Commands

| Command | Description |
|---------|-------------|
| `llar install <module@version>` | Install a module from LLAR Cloud |
| `llar make <module@version>` | Build a module from source |
| `llar test <module@version>` | Verify a module's installed artifacts |

### Flags for `make` and `install`

| Flag | Description |
|------|-------------|
| `-v, --verbose` | Enable verbose build output |
| `-j, --json` | Print the module result as JSON |
| `-o, --output <path>` | Output path (directory, `.zip`, or `.tar.gz`) |

## Development

The repository has three Go modules. The root module contains shared module data and infrastructure, `build` contains the formula runtime and source build engine, and `cmd` contains the complete CLI. The CLI calls the build module directly; install, make, and test remain commands in one executable.

```bash
go test -ldflags="-checklinkname=0" ./...
go -C build test -ldflags="-checklinkname=0" ./...
go -C cmd test -ldflags="-checklinkname=0" ./...
go -C cmd run -ldflags="-checklinkname=0" ./llar --help
```

The submodules currently use local `replace` directives for workspace and CI builds. Standalone module installation requires published dependency versions.

## How It Works

1. **Formula resolution** - LLAR fetches the build formula for the requested module from the formula hub
2. **Dependency resolution** - The formula's `onRequire` callback extracts dependencies, which are resolved using MVS (Minimum Version Selection)
3. **Build** - Dependencies are built first (leaves before roots), then the main module is built via the formula's `onBuild` callback
4. **Caching** - Build results are cached per (module, version, platform) so rebuilds are instant

## LLAR Design

### Version Comparison

```coffee
compareVer (a, b) => {  # version comparison
    ...
}
```

### LLAR Formula

See [LLAR Formula](doc/formula.md).
