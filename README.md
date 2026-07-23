<p align="center">
  <img width="250" height="250" alt="LLAR Logo" src=".github/logo.png" />
</p>

# LLAR

[![Test](https://github.com/goplus/llar/actions/workflows/go.yml/badge.svg)](https://github.com/goplus/llar/actions/workflows/go.yml)
[![codecov](https://codecov.io/gh/goplus/llar/branch/main/graph/badge.svg)](https://codecov.io/gh/goplus/llar)
[![Language](https://img.shields.io/badge/language-XGo-blue.svg)](https://github.com/goplus/gop)

LLAR is a cloud-based multi-language package manager built with [XGo](https://github.com/goplus/gop). It resolves dependencies, downloads source code, and builds libraries from source using declarative formulas.

## Installation

```bash
go install -ldflags="-checklinkname=0" github.com/goplus/llar/cmd/llar@latest
```

## Usage

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
| `llar make <module@version>` | Build a module from source |
| `llar test <module@version>` | Verify a module's installed artifacts |

### Flags for `make`

| Flag | Description |
|------|-------------|
| `-v, --verbose` | Enable verbose build output |
| `-j, --json` | Print the build result as JSON |
| `-o, --output <path>` | Output path (directory or `.zip` file) |

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
