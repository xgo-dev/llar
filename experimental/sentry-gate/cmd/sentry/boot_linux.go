// Copyright 2018 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build linux

package main

// Adapted from runsc/boot/loader.go and pkg/sentry/fsimpl/testutil/kernel.go at
// gVisor Go-export d1e35511e5a41ee5c2afc522d2f9b0c27cf8d382. Only startup for the
// experiment's single guest is retained; gVisor library sources are unchanged.

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/goplus/llar/experimental/sentry-gate/sentryplatform"
	"gvisor.dev/gvisor/pkg/cpuid"
	"gvisor.dev/gvisor/pkg/memutil"
	"gvisor.dev/gvisor/pkg/rand"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/cgroup2fs"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/host"
	"gvisor.dev/gvisor/pkg/sentry/kernel"
	"gvisor.dev/gvisor/pkg/sentry/kernel/auth"
	"gvisor.dev/gvisor/pkg/sentry/loader"
	"gvisor.dev/gvisor/pkg/sentry/pgalloc"
	"gvisor.dev/gvisor/pkg/sentry/platform"
	"gvisor.dev/gvisor/pkg/sentry/seccheck"
	_ "gvisor.dev/gvisor/pkg/sentry/syscalls/linux"
	sentrytime "gvisor.dev/gvisor/pkg/sentry/time"
	"gvisor.dev/gvisor/pkg/sentry/usage"
	"gvisor.dev/gvisor/pkg/sentry/vfs"
	"gvisor.dev/gvisor/pkg/sighandling"
	"gvisor.dev/gvisor/pkg/timing"
)

// newKernel is used once in a dedicated process. Callers exit the process on a
// startup error; Kernel.Release requires a fully initialized kernel and host mount.
func newKernel(inspect sentryplatform.Inspector) (*kernel.Kernel, error) {
	cpuid.Initialize()
	seccheck.Initialize()
	if err := rand.Init(); err != nil {
		return nil, err
	}
	if err := usage.Init(); err != nil {
		return nil, err
	}
	if err := sighandling.IgnoreChildStop(); err != nil {
		return nil, err
	}
	constructor, err := sentryplatform.NewConstructor(inspect)
	if err != nil {
		return nil, err
	}
	p, err := constructor.New(platform.Options{StartupTimer: timing.New("llar-platform", time.Now())})
	if err != nil {
		return nil, err
	}
	k := &kernel.Kernel{Platform: p}

	memoryFD, err := memutil.CreateMemFD("llar-runtime-memory", 0)
	if err != nil {
		return nil, err
	}
	memoryFile := os.NewFile(uintptr(memoryFD), "llar-runtime-memory")
	mf, err := pgalloc.NewMemoryFile(memoryFile, pgalloc.MemoryFileOpts{})
	if err != nil {
		memoryFile.Close()
		return nil, err
	}
	k.SetMemoryFile(mf)
	vdso, err := loader.PrepareVDSO(mf)
	if err != nil {
		return nil, fmt.Errorf("preparing VDSO: %w", err)
	}
	tk := kernel.NewTimekeeper()
	params := kernel.NewVDSOParamPage(mf, vdso.ParamPage.FileRange())
	tk.SetClocks(sentrytime.NewCalibratedClocks(false), params)
	userns := auth.NewRootUserNamespace()
	if err := k.Init(kernel.InitKernelArgs{
		FeatureSet: cpuid.HostFeatureSet().Fixed(),
		Timekeeper: tk, Vdso: vdso, VdsoParams: params,
		RootUserNamespace: userns,
		RootUTSNamespace:  kernel.NewUTSNamespace("", "", userns),
		RootIPCNamespace:  kernel.NewIPCNamespace(userns),
		RootPIDNamespace:  kernel.NewRootPIDNamespace(userns),
		ApplicationCores:  uint(runtime.GOMAXPROCS(0)),
		// Kernel.Init requires this even without container cgroup management.
		Cgroup2FSInit: cgroup2fs.NewFilesystem,
	}); err != nil {
		return nil, fmt.Errorf("initializing kernel: %w", err)
	}

	ctx := k.SupervisorContext()
	hostFS, err := host.NewFilesystem(k.VFS())
	if err != nil {
		return nil, err
	}
	defer hostFS.DecRef(ctx)
	k.SetHostMount(k.VFS().NewDisconnectedMount(hostFS, nil, &vfs.MountOptions{}))
	return k, nil
}
