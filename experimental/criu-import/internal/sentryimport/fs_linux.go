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

//go:build linux && arm64

package sentryimport

// Adapted from runsc/boot/vfs.go, loader.go and loader_test.go at gVisor
// Go-export d1e35511e5a41ee5c2afc522d2f9b0c27cf8d382. The experiment retains a
// read-only DirectFS root, guest procfs, and standard descriptors.

import (
	"fmt"
	"sync"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/fd"
	"gvisor.dev/gvisor/pkg/fspath"
	"gvisor.dev/gvisor/pkg/lisafs"
	"gvisor.dev/gvisor/pkg/sentry/checkpoint"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/gofer"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/proc"
	"gvisor.dev/gvisor/pkg/sentry/kernel"
	"gvisor.dev/gvisor/pkg/sentry/kernel/auth"
	"gvisor.dev/gvisor/pkg/sentry/vfs"
	"gvisor.dev/gvisor/pkg/unet"
	"gvisor.dev/gvisor/runsc/fsgofer"
)

var openProcSelfFD = sync.OnceValue(func() error { return fsgofer.OpenProcSelfFD("/proc/self/fd") })

func startFilesystem(root string) (*fd.FD, func(), error) {
	if err := openProcSelfFD(); err != nil {
		return nil, nil, err
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	client := fd.New(pair[0])
	socket, err := unet.NewSocket(pair[1])
	if err != nil {
		client.Close()
		unix.Close(pair[1])
		return nil, nil, err
	}
	server := lisafs.NewServer()
	impl := fsgofer.NewConnectionImpl(&fsgofer.Config{DonateMountPointFD: true})
	conn, err := server.CreateConnection(socket, root, fsgofer.ConnectionOpts(true), impl)
	if err != nil {
		client.Close()
		socket.Close()
		server.Destroy()
		return nil, nil, err
	}
	server.StartConnection(conn)
	return client, func() {
		socket.Close()
		server.Wait()
		server.Destroy()
	}, nil
}

// mountFilesystem takes ownership of ioFD, which is used by the gofer client.
func mountFilesystem(k *kernel.Kernel, ioFD int) (*vfs.MountNamespace, error) {
	ctx := k.SupervisorContext()
	creds := auth.NewRootCredentials(k.RootUserNamespace())
	vfsObj := k.VFS()
	vfsObj.MustRegisterFilesystemType(gofer.Name, &gofer.FilesystemType{}, &vfs.RegisterFilesystemTypeOptions{})
	vfsObj.MustRegisterFilesystemType(proc.Name, &proc.FilesystemType{}, &vfs.RegisterFilesystemTypeOptions{})
	mntns, err := vfsObj.NewMountNamespace(ctx, creds, "root", gofer.Name, &vfs.MountOptions{
		ReadOnly: true,
		GetFilesystemOptions: vfs.GetFilesystemOptions{
			InternalMount: true,
			InternalData:  gofer.InternalFilesystemOptions{UniqueID: checkpoint.ResourceID{Path: "root"}},
			Data:          fmt.Sprintf("trans=fd,rfdno=%d,wfdno=%d,directfs,disable_fifo_open", ioFD, ioFD),
		},
	}, k)
	if err != nil {
		return nil, fmt.Errorf("mounting root: %w", err)
	}
	root := mntns.Root(ctx)
	defer root.DecRef(ctx)
	_, err = vfsObj.MountAt(ctx, creds, "proc", &vfs.PathOperation{
		Root: root, Start: root, Path: fspath.Parse("proc"), FollowFinalSymlink: true,
	}, proc.Name, &vfs.MountOptions{GetFilesystemOptions: vfs.GetFilesystemOptions{InternalMount: true}})
	if err != nil {
		mntns.DecRef(ctx)
		return nil, fmt.Errorf("mounting proc: %w", err)
	}
	return mntns, nil
}
