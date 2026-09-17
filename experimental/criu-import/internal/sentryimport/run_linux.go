//go:build linux && arm64

package sentryimport

import (
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/abi/linux"
	gcontext "gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/sentry/arch"
	"gvisor.dev/gvisor/pkg/sentry/checkpoint"
	"gvisor.dev/gvisor/pkg/sentry/kernel"
	"gvisor.dev/gvisor/pkg/sentry/kernel/auth"
	"gvisor.dev/gvisor/pkg/sentry/kernel/sched"
	"gvisor.dev/gvisor/pkg/sentry/limits"
	"gvisor.dev/gvisor/pkg/sentry/pgalloc"
	"gvisor.dev/gvisor/pkg/sentry/platform"
	sentrytime "gvisor.dev/gvisor/pkg/sentry/time"
	"gvisor.dev/gvisor/pkg/sentry/vfs"
	"gvisor.dev/gvisor/pkg/timing"
)

// Run converts or restores a checkpoint using the pinned gVisor library.
// Calls in one process must be sequential. The caller must lock its OS thread.
func Run(dir, statePath string, restore bool, backend string) error {
	return run(dir, statePath, restore, backend, "", nil)
}

// RestoreAndExport runs until the guest reaches its return boundary, then
// exports that state as CRIU images and retires the guest.
func RestoreAndExport(dir, statePath, backend, output string, ready func() bool) error {
	return run(dir, statePath, true, backend, output, ready)
}

func run(dir, statePath string, restore bool, backend, output string, ready func() bool) error {
	if dir == "" || statePath == "" {
		return fmt.Errorf("-images and -state are required")
	}
	s, err := readCheckpoint(dir)
	if err != nil {
		return err
	}
	ioFD, stopFS, err := startFilesystem("/")
	if err != nil {
		return err
	}
	defer stopFS()
	defer ioFD.Close()
	var syscalls atomic.Uint64
	k, err := newKernel(func(_ gcontext.Context, _ platform.MemoryManager, ac *arch.Context64) {
		n := syscalls.Add(1)
		if n <= 15 {
			fmt.Fprintf(os.Stderr, "imported syscall %d: host_pid=%d nr=%d ip=%#x args=%v\n", n, os.Getpid(), ac.SyscallNo(), ac.IP(), ac.SyscallArgs())
		}
	}, restore, backend)
	if err != nil {
		return err
	}
	for _, vma := range s.memory.Vmas {
		if vma.GetStart() < uint64(k.MinUserAddress()) || vma.GetEnd() > uint64(k.MaxUserAddress()) {
			return fmt.Errorf("%s cannot import VMA %#x-%#x: platform range %#x-%#x", backend, vma.GetStart(), vma.GetEnd(), k.MinUserAddress(), k.MaxUserAddress())
		}
	}
	if restore {
		fdmap, err := s.openHostFiles()
		if err != nil {
			return err
		}
		fdmap[checkpoint.ResourceID{Path: "root"}] = ioFD.Release()
		ctx := gcontext.WithValues(k.SupervisorContext(), map[any]any{
			vfs.CtxRestoreFilesystemFDMap: fdmap,
			pgalloc.CtxMemoryFileMap:      map[checkpoint.ResourceID]*pgalloc.MemoryFile{},
		})
		f, err := os.Open(statePath)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := k.LoadFrom(ctx, f, nil, nil, nil, sentrytime.NewCalibratedClocks(false), &vfs.CompleteRestoreOptions{}, timing.New("CRIU import restore", time.Now()).Fork("kernel")); err != nil {
			return err
		}
		defer k.Release()
		if err := k.Start(); err != nil {
			return err
		}
		if output != "" {
			exited := make(chan struct{})
			go func() { k.WaitExited(); close(exited) }()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for !ready() {
				select {
				case <-exited:
					return fmt.Errorf("guest exited before return boundary: %s", k.GlobalInit().ExitStatus())
				case <-ticker.C:
				}
			}
			k.Pause()
			k.ReceiveTaskStates()
			err := s.exportCRIU(k, output)
			// The exported image is now the continuation. Retire this copy before
			// an external helper starts the native one.
			k.Kill(linux.WaitStatusExit(0))
			k.Unpause()
			<-exited
			return err
		}
		k.WaitExited()
		status := k.GlobalInit().ExitStatus()
		fmt.Fprintf(os.Stderr, "restored exit=%s syscall_count=%d\n", status, syscalls.Load())
		if !status.Exited() || status.ExitStatus() != 0 {
			return fmt.Errorf("restored process: %s", status)
		}
		return nil
	}
	defer k.Release()
	ctx := k.SupervisorContext()
	mntns, err := mountFilesystem(k, ioFD.Release())
	if err != nil {
		return err
	}
	defer mntns.DecRef(ctx)
	table, fileObjects, err := s.importFiles(k)
	if err != nil {
		return err
	}
	defer table.DecRef(ctx)
	defer func() {
		for _, f := range fileObjects {
			f.DecRef(ctx)
		}
	}()
	ls, err := limits.NewLinuxLimitSet()
	if err != nil {
		return err
	}
	executable := s.files[s.memory.GetExeFileId()].GetReg().GetName()
	creds := auth.NewUserCredentials(auth.KUID(s.threads[0].GetThreadCore().GetCreds().GetUid()), auth.KGID(s.threads[0].GetThreadCore().GetCreds().GetGid()), nil, &auth.TaskCapabilities{}, k.RootUserNamespace())
	mntns.IncRef()
	tg, _, err := k.CreateProcess(kernel.CreateProcessArgs{
		Filename: executable, Argv: []string{executable}, Envv: []string{"GOMAXPROCS=4"}, WorkingDirectory: "/",
		Credentials: creds, FDTable: table, Umask: 0022, Limits: ls,
		MaxSymlinkTraversals: linux.MaxSymlinkTraversals,
		UTSNamespace:         k.RootUTSNamespace(), IPCNamespace: k.RootIPCNamespace(), PIDNamespace: k.RootPIDNamespace(), MountNamespace: mntns,
		StartupTimeline: timing.New("CRIU import template", time.Now()).Fork("kernel"),
	})
	if err != nil {
		return err
	}
	leader := tg.Leader()
	if err := s.importMemory(ctx, leader.MemoryManager(), fileObjects); err != nil {
		return err
	}
	ids := make(map[int64]int64)
	for i, core := range s.threads {
		t := leader
		if i != 0 {
			image, err := leader.TaskImage().Fork(ctx, k, true)
			if err != nil {
				return err
			}
			fsctx := leader.FSContext()
			fsctx.IncRef()
			table.IncRef()
			creds.UserNamespace.IncRef()
			mntns.IncRef()
			k.RootUTSNamespace().IncRef()
			k.RootIPCNamespace().IncRef()
			k.RootCgroupNamespace().IncRef()
			k.RootNetworkNamespace().IncRef()
			t, err = k.TaskSet().NewTask(ctx, &kernel.TaskConfig{
				Kernel: k, ThreadGroup: tg, TaskImage: image, FSContext: fsctx, FDTable: table, Credentials: creds,
				NetworkNamespace: k.RootNetworkNamespace(), AllowedCPUMask: sched.NewFullCPUSet(uint(runtime.GOMAXPROCS(0))),
				UTSNamespace: k.RootUTSNamespace(), IPCNamespace: k.RootIPCNamespace(), CgroupNamespace: k.RootCgroupNamespace(), MountNamespace: mntns,
				UserCounters: k.GetUserCounters(creds.RealKUID), Personality: linux.PER_LINUX,
			})
			if err != nil {
				return err
			}
		}
		if err := importThread(t, core, backend); err != nil {
			return err
		}
		ids[int64(t.ThreadID())] = int64(s.process.Threads[i])
	}
	if err := s.importSignals(tg); err != nil {
		return err
	}
	raw, err := os.Create(statePath + ".template")
	if err != nil {
		return err
	}
	k.Pause()
	if err := k.SaveTo(ctx, raw, nil, nil, false, true); err != nil {
		return fmt.Errorf("save template: %w", err)
	}
	k.BeforeResume(ctx)
	if err := rewriteIDs(statePath+".template", statePath, ids, s.process); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "converted pid=%d threads=%v maps=%d state=%s\n", s.process.GetPid(), s.process.Threads, len(s.memory.Vmas), statePath)
	return nil
}
