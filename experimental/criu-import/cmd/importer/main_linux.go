//go:build linux && arm64

package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/goplus/llar/experimental/criu-import/internal/sentryimport"
	"gvisor.dev/gvisor/pkg/log"
)

func main() {
	dir := flag.String("images", "", "CRIU checkpoint directory")
	statePath := flag.String("state", "", "converted Sentry state path")
	restore := flag.Bool("restore", false, "restore the converted state")
	backend := flag.String("platform", "systrap", "gVisor execution platform")
	flag.Parse()
	log.SetLevel(log.Warning)
	runtime.LockOSThread()
	if err := sentryimport.Run(*dir, *statePath, *restore, *backend); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
