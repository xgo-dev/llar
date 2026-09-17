package main

import (
	"embed"
	"fmt"
	"os"
	"strings"

	formulapkg "github.com/goplus/llar/formula"
	internalformula "github.com/goplus/llar/internal/formula"
)

//go:embed formula/Probe_llar.gox
var formulaFiles embed.FS

func main() {
	loaded, err := internalformula.LoadFS(formulaFiles, "formula/Probe_llar.gox")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if loaded.OnBuild == nil {
		fmt.Fprintln(os.Stderr, "formula has no onBuild callback")
		os.Exit(1)
	}

	project := &formulapkg.Project{SourceFS: formulaFiles}
	ctx := formulapkg.NewContext(project, "/", "/tmp", "linux", nil)
	loaded.OnBuild(ctx)
	if err := ctx.Errs.ToError(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	metadata := ctx.Out.Metadata()
	if !strings.Contains(metadata, "localhost") {
		fmt.Fprintf(os.Stderr, "formula metadata does not contain localhost: %q\n", metadata)
		os.Exit(1)
	}
	fmt.Printf("formula %s@%s ran through ixgo; metadata=%d bytes\n", loaded.ModPath, loaded.FromVer, len(metadata))
}
