//go:build linux

// This executable boots Sentry directly and runs a guest under Systrap.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/abi/linux"
	gcontext "gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/sentry/arch"
	"gvisor.dev/gvisor/pkg/sentry/kernel"
	"gvisor.dev/gvisor/pkg/sentry/kernel/auth"
	"gvisor.dev/gvisor/pkg/sentry/limits"
	"gvisor.dev/gvisor/pkg/sentry/platform"
	"gvisor.dev/gvisor/pkg/sentry/watchdog"
	"gvisor.dev/gvisor/pkg/sighandling"
	"gvisor.dev/gvisor/pkg/timing"
)

func main() {
	root := flag.String("root", "/", "read-only host directory used as the guest root")
	probe := flag.String("probe", "", "absolute guest executable path")
	flag.Parse()
	log.SetLevel(log.Warning)
	runtime.LockOSThread()
	if err := runSentry(*root, *probe); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runSentry(root, probe string) error {
	if probe == "" {
		return errors.New("-probe is required")
	}
	ioFD, stopFilesystem, err := startFilesystem(root)
	if err != nil {
		return err
	}
	defer stopFilesystem()
	defer ioFD.Close()

	var observed atomic.Uint64
	k, err := newKernel(func(_ gcontext.Context, _ platform.MemoryManager, ac *arch.Context64) {
		n := observed.Add(1)
		if n <= 12 {
			fmt.Printf("llar inspector pid=%d syscall=%d nr=%d ip=%#x args=%v\n", os.Getpid(), n, ac.SyscallNo(), ac.IP(), ac.SyscallArgs())
		}
	})
	if err != nil {
		return err
	}
	defer k.Release()
	ctx := k.SupervisorContext()

	mntns, err := mountFilesystem(k, ioFD.Release())
	if err != nil {
		return err
	}
	defer mntns.DecRef(ctx)

	fdt, err := importStdio(k)
	if err != nil {
		return err
	}
	defer fdt.DecRef(ctx)
	ls, err := limits.NewLinuxLimitSet()
	if err != nil {
		return err
	}
	timeline := timing.New("llar-runtime", time.Now()).Fork("guest")
	defer timeline.End()

	// CreateProcess takes ownership of one mount-namespace reference. The local
	// reference above remains valid for cleanup on both success and failure.
	mntns.IncRef()
	tg, _, err := k.CreateProcess(kernel.CreateProcessArgs{
		Filename: probe, Argv: []string{probe},
		Envv: []string{"GOMAXPROCS=2"}, WorkingDirectory: "/",
		Credentials: auth.NewUserCredentials(1000, 1000, nil, &auth.TaskCapabilities{}, k.RootUserNamespace()),
		FDTable:     fdt, Umask: 0022, Limits: ls,
		MaxSymlinkTraversals: linux.MaxSymlinkTraversals,
		UTSNamespace:         k.RootUTSNamespace(), IPCNamespace: k.RootIPCNamespace(),
		PIDNamespace: k.RootPIDNamespace(), MountNamespace: mntns,
		StartupTimeline: timeline,
	})
	if err != nil {
		return fmt.Errorf("loading guest: %w", err)
	}

	stopSignals := sighandling.StartSignalForwarding(func(sig linux.Signal) {
		if err := k.SendExternalSignalThreadGroup(tg, kernel.SignalInfoPriv(sig)); err != nil {
			log.Warningf("forwarding signal %d: %v", sig, err)
		}
	})
	defer stopSignals()
	dog := watchdog.New(k, watchdog.DefaultOpts)
	dog.Start()
	defer dog.Stop()
	if err := k.Start(); err != nil {
		return err
	}
	k.WaitExited()
	status := tg.ExitStatus()
	if !status.Exited() || status.ExitStatus() != 0 {
		return fmt.Errorf("application exited with %s", status)
	}
	fmt.Printf("Sentry pid=%d: application exited successfully; observed=%d syscalls\n", os.Getpid(), observed.Load())
	return nil
}
