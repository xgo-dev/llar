//go:build linux && arm64

package sentryimport

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/checkpoint-restore/go-criu/v8/crit"
	core "github.com/checkpoint-restore/go-criu/v8/crit/images/criu-core"
	sa "github.com/checkpoint-restore/go-criu/v8/crit/images/criu-sa"
	"github.com/checkpoint-restore/go-criu/v8/crit/images/eventpoll"
	"github.com/checkpoint-restore/go-criu/v8/crit/images/fdinfo"
	mmimage "github.com/checkpoint-restore/go-criu/v8/crit/images/mm"
	"github.com/checkpoint-restore/go-criu/v8/crit/images/pagemap"
	"github.com/checkpoint-restore/go-criu/v8/crit/images/pstree"
	"github.com/checkpoint-restore/go-criu/v8/crit/images/vma"
	"google.golang.org/protobuf/proto"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/errors/linuxerr"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/sentry/kernel"
	"gvisor.dev/gvisor/pkg/sentry/memmap"
	"gvisor.dev/gvisor/pkg/sentry/vfs"
	"gvisor.dev/gvisor/pkg/state/wire"
	"gvisor.dev/gvisor/pkg/usermem"
	"gvisor.dev/gvisor/pkg/waiter"
)

func writeImage(dir, name, magic string, entries ...proto.Message) error {
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	defer f.Close()
	img := &crit.CriuImage{Magic: magic}
	for _, e := range entries {
		img.Entries = append(img.Entries, &crit.CriuEntry{Message: e})
	}
	return crit.New(nil, f, dir, false, false).Encode(img)
}

// exportCRIU is called with the kernel paused. It regenerates application
// memory and thread cores, while retaining the entry image's namespace/files
// identities. Unsupported changes are rejected instead of reverting to entry data.
func (s *checkpointImage) exportCRIU(k *kernel.Kernel, out string) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("exporting pinned Sentry state: %v", p)
		}
	}()
	if err := os.Mkdir(out, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".img") || strings.HasPrefix(e.Name(), "pages-") {
			continue
		}
		// The guest clock is aligned to this host. Retain the running native
		// clock on return instead of rewinding it to the entry checkpoint.
		// CRIU treats an absent timens-0 image as this case.
		if e.Name() == "timens-0.img" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, e.Name()), data, 0600); err != nil {
			return err
		}
	}
	ctx := k.SupervisorContext()
	leader := k.GlobalInit().Leader()
	m := leader.MemoryManager()
	var vmas []*vma.VmaEntry
	m.ReadMapsDataInto(ctx, func(start, end hostarch.Addr, perms hostarch.AccessType, private string, offset uint64, _, _ uint32, _ uint64, _ string) {
		// procfs includes a synthetic vsyscall entry that is not an application
		// VMA. It must not become a native mmap request on ARM64.
		if start < k.MinUserAddress() || end > k.MaxUserAddress() {
			return
		}
		var saved *vma.VmaEntry
		for _, old := range s.memory.Vmas {
			if uint64(start) >= old.GetStart() && uint64(end) <= old.GetEnd() {
				saved = proto.Clone(old).(*vma.VmaEntry)
				saved.Pgoff = proto.Uint64(old.GetPgoff() + uint64(start) - old.GetStart())
				break
			}
		}
		if saved == nil {
			if private != "p" {
				panic("new shared mappings are not supported")
			}
			saved = &vma.VmaEntry{Pgoff: proto.Uint64(offset), Shmid: proto.Uint64(0), Flags: proto.Uint32(linux.MAP_PRIVATE | linux.MAP_ANONYMOUS), Status: proto.Uint32(513), Fd: proto.Int64(-1)}
		}
		saved.Start = proto.Uint64(uint64(start))
		saved.End = proto.Uint64(uint64(end))
		saved.Prot = proto.Uint32(uint32(perms.Prot()))
		vmas = append(vmas, saved)
	})
	for _, v := range vmas {
		m.Invalidate(hostarch.AddrRange{Start: hostarch.Addr(v.GetStart()), End: hostarch.Addr(v.GetEnd())}, memmap.InvalidateOpts{})
	}
	statePath := filepath.Join(out, "sentry.state")
	stateFile, err := os.Create(statePath)
	if err != nil {
		return err
	}
	if err := k.SaveTo(ctx, stateFile, nil, nil, false, true); err != nil {
		return err
	}
	k.BeforeResume(ctx)
	f, err := os.Open(statePath)
	if err != nil {
		return err
	}
	defer f.Close()
	r := &wire.Reader{Reader: f}
	if _, err := readGraph(r); err != nil {
		return err
	}
	g, err := readGraph(r)
	if err != nil {
		return err
	}
	states := g.taskStates()
	tasks := k.RootPIDNamespace().Tasks()
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ThreadID() < tasks[j].ThreadID() })
	return s.writeReturnImages(k, out, g, states, tasks, vmas)
}

func (s *checkpointImage) writeReturnImages(k *kernel.Kernel, out string, g *stateGraph, states map[int64]*wire.Struct, tasks []*kernel.Task, vmas []*vma.VmaEntry) error {
	ctx := k.SupervisorContext()
	leader := k.GlobalInit().Leader()
	m := leader.MemoryManager()
	tree := proto.Clone(s.process).(*pstree.PstreeEntry)
	tree.Threads = nil
	for _, t := range tasks {
		if t.ThreadGroup() != k.GlobalInit() {
			return fmt.Errorf("multiple process export is not supported")
		}
		tid := t.ThreadID()
		tree.Threads = append(tree.Threads, uint32(tid))
		base := s.threads[0]
		if t != leader {
			base = s.threads[1]
		}
		c := proto.Clone(base).(*core.CoreEntry)
		if t != leader {
			c.Tc = nil
		}
		ts := states[int64(tid)]
		ac := t.Arch().Fork()
		if g.boolean(*g.field(ts, "haveSyscallReturn")) {
			if restart, ok := linuxerr.SyscallRestartErrorFromReturn(ac.Return()); ok {
				if restart == linuxerr.ERESTART_RESTARTBLOCK {
					ac.SetReturn(^uintptr(linuxerr.EINTR.Errno() - 1))
				} else {
					ac.RestartSyscall()
				}
			}
		}
		i := c.GetTiAarch64()
		i.Gpregs.Regs = append([]uint64(nil), ac.Regs.Regs[:]...)
		i.Gpregs.Sp = proto.Uint64(ac.Regs.Sp)
		i.Gpregs.Pc = proto.Uint64(ac.Regs.Pc)
		i.Gpregs.Pstate = proto.Uint64(ac.Regs.Pstate)
		i.Tls = proto.Uint64(uint64(ac.TLS()))
		i.ClearTidAddr = proto.Uint64(uint64(g.number(*g.field(ts, "cleartid"))))
		fp := *ac.FloatingPointData()
		for j := range i.Fpsimd.Vregs {
			i.Fpsimd.Vregs[j] = binary.LittleEndian.Uint64(fp[j*8:])
		}
		i.Fpsimd.Fpsr = proto.Uint32(binary.LittleEndian.Uint32(fp[512:]))
		i.Fpsimd.Fpcr = proto.Uint32(binary.LittleEndian.Uint32(fp[516:]))
		mask := t.SignalMask()
		if g.boolean(*g.field(ts, "haveSavedSignalMask")) {
			mask = linux.SignalSet(g.number(*g.field(ts, "savedSignalMask")))
		}
		c.ThreadCore.BlkSigset = proto.Uint64(uint64(mask))
		c.ThreadCore.FutexRla = proto.Uint64(uint64(t.GetRobustList()))
		stack := t.SignalStack()
		c.ThreadCore.Sas = &core.ThreadSasEntry{SsSp: proto.Uint64(stack.Addr), SsSize: proto.Uint64(stack.Size), SsFlags: proto.Uint32(stack.Flags)}
		creds := t.Credentials()
		cc := c.ThreadCore.Creds
		cc.Uid = proto.Uint32(uint32(creds.RealKUID))
		cc.Euid = proto.Uint32(uint32(creds.EffectiveKUID))
		cc.Suid = proto.Uint32(uint32(creds.SavedKUID))
		cc.Fsuid = proto.Uint32(uint32(creds.EffectiveKUID))
		cc.Gid = proto.Uint32(uint32(creds.RealKGID))
		cc.Egid = proto.Uint32(uint32(creds.EffectiveKGID))
		cc.Sgid = proto.Uint32(uint32(creds.SavedKGID))
		cc.Fsgid = proto.Uint32(uint32(creds.EffectiveKGID))
		cc.CapPrm = []uint32{uint32(creds.PermittedCaps), uint32(uint64(creds.PermittedCaps) >> 32)}
		cc.CapEff = []uint32{uint32(creds.EffectiveCaps), uint32(uint64(creds.EffectiveCaps) >> 32)}
		cc.CapInh = []uint32{uint32(creds.InheritableCaps), uint32(uint64(creds.InheritableCaps) >> 32)}
		cc.CapBnd = []uint32{uint32(creds.BoundingCaps), uint32(uint64(creds.BoundingCaps) >> 32)}
		cc.Groups = nil
		for _, gid := range creds.ExtraKGIDs {
			cc.Groups = append(cc.Groups, uint32(gid))
		}
		if t.PendingSignals() != 0 {
			return fmt.Errorf("pending signals on TID %d require export support", tid)
		}
		if t == leader {
			c.Tc.Sigactions = nil
			for sig := linux.Signal(1); sig <= 64; sig++ {
				if sig == linux.SIGKILL || sig == linux.SIGSTOP {
					continue
				}
				a, err := k.GlobalInit().SetSigAction(sig, nil)
				if err != nil {
					return err
				}
				c.Tc.Sigactions = append(c.Tc.Sigactions, &sa.SaEntry{Sigaction: proto.Uint64(a.Handler), Flags: proto.Uint64(a.Flags), Restorer: proto.Uint64(a.Restorer), Mask: proto.Uint64(uint64(a.Mask))})
			}
		}
		if err := writeImage(out, fmt.Sprintf("core-%d.img", tid), "CORE", c); err != nil {
			return err
		}
	}
	if err := writeImage(out, "pstree.img", "PSTREE", tree); err != nil {
		return err
	}
	memory := proto.Clone(s.memory).(*mmimage.MmEntry)
	memory.Vmas = vmas
	memory.MmArgStart = proto.Uint64(uint64(m.ArgvStart()))
	memory.MmArgEnd = proto.Uint64(uint64(m.ArgvEnd()))
	memory.MmEnvStart = proto.Uint64(uint64(m.EnvvStart()))
	memory.MmEnvEnd = proto.Uint64(uint64(m.EnvvEnd()))
	managers := g.structures("pkg/sentry/mm.MemoryManager")
	if len(managers) != 1 {
		return fmt.Errorf("expected one memory manager")
	}
	brk := g.object(*g.field(managers[0], "brk")).(*wire.Struct)
	memory.MmBrk = proto.Uint64(uint64(g.number(*g.field(brk, "End"))))
	if err := writeImage(out, fmt.Sprintf("mm-%d.img", tree.GetPid()), "MM", memory); err != nil {
		return err
	}
	pages, err := os.Create(filepath.Join(out, "pages-1.img"))
	if err != nil {
		return err
	}
	defer pages.Close()
	pageEntries := []proto.Message{&pagemap.PagemapHead{PagesId: proto.Uint32(1)}}
	set := g.object(*g.field(managers[0], "pmas")).(*wire.Struct)
	var total uint64
	for _, obj := range g.items(*g.field(set, "root")) {
		seg := g.object(obj).(*wire.Struct)
		start, end := uint64(g.number(*g.field(seg, "Start"))), uint64(g.number(*g.field(seg, "End")))
		pma := g.object(*g.field(seg, "Value")).(*wire.Struct)
		if !g.boolean(*g.field(pma, "private")) {
			return fmt.Errorf("unexpected non-private PMA")
		}
		pageEntries = append(pageEntries, &pagemap.PagemapEntry{Vaddr: proto.Uint64(start), CompatNrPages: proto.Uint32(uint32((end - start) / 4096)), Flags: proto.Uint32(4)})
		buf := make([]byte, 64<<10)
		for at := start; at < end; {
			n := min(uint64(len(buf)), end-at)
			read, err := m.CopyIn(ctx, hostarch.Addr(at), buf[:n], usermem.IOOpts{IgnorePermissions: true})
			if err != nil || uint64(read) != n {
				return fmt.Errorf("export page %#x: %d/%d %v", at, read, n, err)
			}
			if _, err := pages.Write(buf[:n]); err != nil {
				return err
			}
			at += n
			total += n
		}
	}
	if err := writeImage(out, fmt.Sprintf("pagemap-%d.img", tree.GetPid()), "PAGEMAP", pageEntries...); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "exported guest pid=%d threads=%v maps=%d bytes=%d\n", tree.GetPid(), tree.Threads, len(vmas), total)
	return s.exportFiles(k, out, g, states[int64(leader.ThreadID())])
}

func (s *checkpointImage) exportFiles(k *kernel.Kernel, out string, g *stateGraph, task *wire.Struct) error {
	ctx := k.SupervisorContext()
	leader := k.GlobalInit().Leader()
	actual := leader.FDTable().GetFDs(ctx)
	if len(actual) != len(s.fds) {
		return fmt.Errorf("FD set changed during callback")
	}
	implementations := g.fdImplementations(task)
	all, err := decodeImage(s.dir, "files.img", &fdinfo.FileEntry{})
	if err != nil {
		return err
	}
	byID := make(map[uint32]*fdinfo.FileEntry)
	for _, f := range all {
		byID[f.GetId()] = f
	}
	seen := make(map[uint32]bool)
	for _, old := range s.fds {
		fd, flags := leader.FDTable().Get(int32(old.GetFd()))
		if fd == nil {
			return fmt.Errorf("FD %d disappeared", old.GetFd())
		}
		if (flags.CloseOnExec) != (old.GetFlags()&linux.FD_CLOEXEC != 0) {
			return fmt.Errorf("FD flags changed")
		}
		if seen[old.GetId()] {
			fd.DecRef(ctx)
			continue
		}
		seen[old.GetId()] = true
		f := byID[old.GetId()]
		impl := implementations[int64(old.GetFd())]
		switch f.GetType() {
		case fdinfo.FdTypes_REG:
			f.Reg.Flags = proto.Uint32(fd.StatusFlags())
			pos, err := fd.Seek(ctx, 0, linux.SEEK_CUR)
			if err != nil {
				return err
			}
			f.Reg.Pos = proto.Uint64(uint64(pos))
			st, err := fd.Stat(ctx, vfs.StatOptions{Mask: linux.STATX_SIZE})
			if err != nil {
				return err
			}
			if f.Reg.Size != nil {
				f.Reg.Size = proto.Uint64(st.Size)
			}
		case fdinfo.FdTypes_EVENTFD:
			f.Efd.Counter = proto.Uint64(uint64(g.number(*g.field(impl, "val"))))
		case fdinfo.FdTypes_EVENTPOLL:
			f.Epfd.Tfd = nil
			interest := g.object(*g.field(impl, "interest")).(*wire.Map)
			for _, value := range interest.Values {
				i := g.object(value).(*wire.Struct)
				key := g.object(*g.field(i, "key")).(*wire.Struct)
				data := g.items(*g.field(i, "userData"))
				bits := uint64(uint32(g.number(data[0]))) | uint64(uint32(g.number(data[1])))<<32
				f.Epfd.Tfd = append(f.Epfd.Tfd, &eventpoll.EventpollTfdEntry{Id: proto.Uint32(0), Tfd: proto.Uint32(uint32(g.number(*g.field(key, "num")))), Events: proto.Uint32(uint32(g.number(*g.field(i, "mask")))), Data: proto.Uint64(bits)})
			}
		case fdinfo.FdTypes_PIPE:
			if fd.StatusFlags()&linux.O_ACCMODE == linux.O_RDONLY && fd.Readiness(waiter.ReadableEvents)&waiter.ReadableEvents != 0 {
				return fmt.Errorf("nonempty pipe export is unsupported")
			}
		default:
			return fmt.Errorf("unsupported export FD type %s", f.GetType())
		}
		fd.DecRef(ctx)
	}
	var entries []proto.Message
	for _, f := range all {
		entries = append(entries, f)
	}
	return writeImage(out, "files.img", "FILES", entries...)
}
