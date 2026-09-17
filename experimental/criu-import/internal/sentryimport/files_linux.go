//go:build linux && arm64

package sentryimport

import (
	"fmt"

	"github.com/checkpoint-restore/go-criu/v8/crit/images/fdinfo"
	pipedata "github.com/checkpoint-restore/go-criu/v8/crit/images/pipe-data"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/sentry/checkpoint"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/eventfd"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/host"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/pipefs"
	"gvisor.dev/gvisor/pkg/sentry/kernel"
	"gvisor.dev/gvisor/pkg/sentry/vfs"
)

func fileKey(id uint32) checkpoint.ResourceID {
	return checkpoint.ResourceID{Path: fmt.Sprintf("criu-file:%d", id)}
}

func (s *checkpointImage) openHostFiles() (map[checkpoint.ResourceID]int, error) {
	files := make(map[checkpoint.ResourceID]int)
	for id, f := range s.files {
		if f.GetType() != fdinfo.FdTypes_REG {
			continue
		}
		reg := f.GetReg()
		flags := int(reg.GetFlags()) & (unix.O_ACCMODE | unix.O_APPEND | unix.O_NONBLOCK | unix.O_DIRECTORY)
		// CRIU records shared file mappings with a read-only backing handle,
		// even if the original mapping is writable.
		for _, v := range s.memory.Vmas {
			if v.GetShmid() == uint64(id) && v.GetFlags()&linux.MAP_SHARED != 0 && v.GetProt()&linux.PROT_WRITE != 0 {
				flags = (flags &^ unix.O_ACCMODE) | unix.O_RDWR
			}
		}
		fd, err := unix.Open(reg.GetName(), flags|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("open CRIU file %d %q: %w", id, reg.GetName(), err)
		}
		files[fileKey(id)] = fd
	}
	return files, nil
}

func (s *checkpointImage) importFiles(k *kernel.Kernel) (*kernel.FDTable, map[uint32]*vfs.FileDescription, error) {
	ctx := k.SupervisorContext()
	hostFiles, err := s.openHostFiles()
	if err != nil {
		return nil, nil, err
	}
	objects := make(map[uint32]*vfs.FileDescription)
	pipes := make(map[uint32][2]*vfs.FileDescription)
	data, err := decodeImage(s.dir, "pipes-data.img", &pipedata.PipeDataEntry{})
	if err != nil {
		return nil, nil, err
	}
	for _, d := range data {
		if d.GetBytes() != 0 {
			return nil, nil, fmt.Errorf("nonempty pipe %d is outside this experiment", d.GetPipeId())
		}
	}
	for id, f := range s.files {
		var obj *vfs.FileDescription
		switch f.GetType() {
		case fdinfo.FdTypes_REG:
			obj, err = host.NewFD(ctx, k.HostMount(), hostFiles[fileKey(id)], &host.NewFDOptions{
				Savable: true, Restorable: true, RestoreKey: fileKey(id),
			})
			if err == nil && f.GetReg().GetPos() != 0 {
				_, err = obj.Seek(ctx, int64(f.GetReg().GetPos()), linux.SEEK_SET)
			}
		case fdinfo.FdTypes_EVENTFD:
			obj, err = eventfd.New(ctx, k.VFS(), f.GetEfd().GetCounter(), false, f.GetEfd().GetFlags())
		case fdinfo.FdTypes_EVENTPOLL:
			obj, err = k.VFS().NewEpollInstanceFD(ctx)
		case fdinfo.FdTypes_PIPE:
			p := f.GetPipe()
			pair, ok := pipes[p.GetPipeId()]
			if !ok {
				pair[0], pair[1], err = pipefs.NewConnectedPipeFDs(ctx, k.PipeMount(), linux.O_NONBLOCK)
				if err != nil {
					return nil, nil, err
				}
				pipes[p.GetPipeId()] = pair
			}
			obj = pair[p.GetFlags()&linux.O_ACCMODE]
		default:
			return nil, nil, fmt.Errorf("unsupported CRIU file type %s", f.GetType())
		}
		if err != nil {
			return nil, nil, fmt.Errorf("file %d: %w", id, err)
		}
		objects[id] = obj
	}
	table := k.NewFDTable()
	byFD := make(map[uint32]*vfs.FileDescription)
	for _, f := range s.fds {
		obj := objects[f.GetId()]
		if obj == nil {
			return nil, nil, fmt.Errorf("missing object for FD %d", f.GetFd())
		}
		if _, err := table.NewFDAt(ctx, int32(f.GetFd()), obj, kernel.FDFlags{CloseOnExec: f.GetFlags()&linux.FD_CLOEXEC != 0}); err != nil {
			return nil, nil, err
		}
		byFD[f.GetFd()] = obj
	}
	for id, f := range s.files {
		if f.GetType() != fdinfo.FdTypes_EVENTPOLL {
			continue
		}
		ep := objects[id].Impl().(*vfs.EpollInstance)
		for _, watch := range f.GetEpfd().Tfd {
			obj := byFD[watch.GetTfd()]
			if obj == nil {
				return nil, nil, fmt.Errorf("missing epoll target FD %d", watch.GetTfd())
			}
			value := watch.GetData()
			event := linux.EpollEvent{Events: watch.GetEvents(), Data: [2]int32{int32(value), int32(value >> 32)}}
			if err := ep.AddInterest(obj, int32(watch.GetTfd()), event); err != nil {
				return nil, nil, fmt.Errorf("epoll FD %d: %w", watch.GetTfd(), err)
			}
		}
	}
	return table, objects, nil
}
