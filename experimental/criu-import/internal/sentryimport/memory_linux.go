//go:build linux && arm64

package sentryimport

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/checkpoint-restore/go-criu/v8/crit"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/sentry/arch"
	"gvisor.dev/gvisor/pkg/sentry/memmap"
	"gvisor.dev/gvisor/pkg/sentry/mm"
	"gvisor.dev/gvisor/pkg/sentry/vfs"
	"gvisor.dev/gvisor/pkg/usermem"
)

func (s *checkpointImage) importMemory(ctx context.Context, m *mm.MemoryManager, files map[uint32]*vfs.FileDescription) error {
	var old []hostarch.AddrRange
	m.ReadMapsDataInto(ctx, func(start, end hostarch.Addr, _ hostarch.AccessType, _ string, _ uint64, _, _ uint32, _ uint64, _ string) {
		old = append(old, hostarch.AddrRange{Start: start, End: end})
	})
	for _, r := range old {
		if err := m.MUnmap(ctx, r.Start, uint64(r.Length())); err != nil {
			return err
		}
	}
	for _, v := range s.memory.Vmas {
		opts := memmap.MMapOpts{
			Addr: hostarch.Addr(v.GetStart()), Length: v.GetEnd() - v.GetStart(), Fixed: true,
			Private: v.GetFlags()&linux.MAP_SHARED == 0, Perms: hostarch.ReadWrite, MaxPerms: hostarch.AnyAccess,
		}
		if v.GetShmid() != 0 {
			file := files[uint32(v.GetShmid())]
			if file == nil {
				return fmt.Errorf("missing VMA backing object %d", v.GetShmid())
			}
			opts.Offset = v.GetPgoff()
			if err := file.ConfigureMMap(ctx, &opts); err != nil {
				return err
			}
		}
		if _, err := m.MMap(ctx, opts); err != nil {
			return fmt.Errorf("mmap %#x-%#x: %w", v.GetStart(), v.GetEnd(), err)
		}
	}
	reader, err := crit.NewMemoryReader(s.dir, s.process.GetPid(), hostarch.PageSize)
	if err != nil {
		return err
	}
	pages, err := os.Open(filepath.Join(s.dir, fmt.Sprintf("pages-%d.img", reader.GetPagesID())))
	if err != nil {
		return err
	}
	defer pages.Close()
	for _, e := range reader.GetPagemapEntries() {
		if e.GetInParent() || e.GetFlags()&4 == 0 {
			return fmt.Errorf("only full, locally stored page images are supported")
		}
		count := e.GetNrPages()
		if e.NrPages == nil {
			count = uint64(e.GetCompatNrPages())
		} // CRIU 3.17 stores field 2.
		data := make([]byte, count*hostarch.PageSize)
		if _, err := io.ReadFull(pages, data); err != nil {
			return err
		}
		if n, err := m.CopyOut(ctx, hostarch.Addr(e.GetVaddr()), data, usermem.IOOpts{}); err != nil || n != len(data) {
			return fmt.Errorf("write snapshot page %#x: %d/%d: %v", e.GetVaddr(), n, len(data), err)
		}
	}
	for _, v := range s.memory.Vmas {
		if v.GetStatus()&8 != 0 { // CRIU VMA_AREA_VDSO.
			data := make([]byte, v.GetEnd()-v.GetStart())
			if _, err := m.CopyIn(ctx, hostarch.Addr(v.GetStart()), data, usermem.IOOpts{}); err != nil {
				return err
			}
			e, err := elf.NewFile(bytes.NewReader(data))
			if err != nil {
				return fmt.Errorf("parse original VDSO: %w", err)
			}
			syms, err := e.DynamicSymbols()
			if err != nil {
				return err
			}
			calls := map[string]uint32{
				"__kernel_clock_gettime": unix.SYS_CLOCK_GETTIME,
				"__kernel_clock_getres":  unix.SYS_CLOCK_GETRES,
				"__kernel_gettimeofday":  unix.SYS_GETTIMEOFDAY,
				"__kernel_getrandom":     unix.SYS_GETRANDOM,
			}
			for _, symbol := range syms {
				if symbol.Name == "__kernel_rt_sigreturn" {
					m.SetVDSOSigReturn(v.GetStart() + symbol.Value)
				}
				nr, ok := calls[symbol.Name]
				if !ok {
					continue
				}
				if symbol.Size < 12 {
					return fmt.Errorf("VDSO symbol %s is too small for syscall trampoline", symbol.Name)
				}
				// Preserve cached function addresses; use Sentry syscalls instead of
				// the source kernel's live VVAR data.
				var code [12]byte
				binary.LittleEndian.PutUint32(code[0:4], 0xd2800008|nr<<5) // mov x8, #nr
				binary.LittleEndian.PutUint32(code[4:8], 0xd4000001)       // svc #0
				binary.LittleEndian.PutUint32(code[8:12], 0xd65f03c0)      // ret
				if _, err := m.CopyOut(ctx, hostarch.Addr(v.GetStart()+symbol.Value), code[:], usermem.IOOpts{}); err != nil {
					return err
				}
			}
		}
		perms := hostarch.AccessType{Read: v.GetProt()&linux.PROT_READ != 0, Write: v.GetProt()&linux.PROT_WRITE != 0, Execute: v.GetProt()&linux.PROT_EXEC != 0}
		if err := m.MProtect(hostarch.Addr(v.GetStart()), v.GetEnd()-v.GetStart(), perms, false); err != nil {
			return err
		}
	}
	m.BrkSetup(ctx, hostarch.Addr(s.memory.GetMmStartBrk()))
	if _, err := m.Brk(ctx, hostarch.Addr(s.memory.GetMmBrk())); err != nil {
		return err
	}
	m.SetArgvStart(hostarch.Addr(s.memory.GetMmArgStart()))
	m.SetArgvEnd(hostarch.Addr(s.memory.GetMmArgEnd()))
	m.SetEnvvStart(hostarch.Addr(s.memory.GetMmEnvStart()))
	m.SetEnvvEnd(hostarch.Addr(s.memory.GetMmEnvEnd()))
	var auxv arch.Auxv
	for i := 0; i+1 < len(s.memory.MmSavedAuxv); i += 2 {
		auxv = append(auxv, arch.AuxEntry{Key: s.memory.MmSavedAuxv[i], Value: hostarch.Addr(s.memory.MmSavedAuxv[i+1])})
	}
	m.SetAuxv(auxv)
	// host.inode.InvalidateUnsavable is a no-op. Drop clean file-backed PMAs
	// explicitly: SaveTo only accepts MemoryFile-backed PMAs. Private COW pages
	// contain the imported snapshot bytes and must remain intact.
	for _, v := range s.memory.Vmas {
		m.Invalidate(hostarch.AddrRange{Start: hostarch.Addr(v.GetStart()), End: hostarch.Addr(v.GetEnd())}, memmap.InvalidateOpts{InvalidatePrivate: false})
	}
	return nil
}
