//go:build linux

package sentryplatform

import (
	"errors"
	"testing"

	"gvisor.dev/gvisor/pkg/abi/linux"
	gcontext "gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/sentry/arch"
	"gvisor.dev/gvisor/pkg/sentry/platform"
)

type fakePlatform struct {
	platform.Platform
	ctx platform.Context
}

func (p *fakePlatform) NewContext(gcontext.Context) platform.Context {
	return p.ctx
}

type fakeContext struct {
	platform.Context
	switchToApp func(gcontext.Context, platform.MemoryManager, *arch.Context64, int32) (*linux.SignalInfo, hostarch.AccessType, error)
}

func (c *fakeContext) Switch(ctx gcontext.Context, mm platform.MemoryManager, ac *arch.Context64, cpu int32) (*linux.SignalInfo, hostarch.AccessType, error) {
	return c.switchToApp(ctx, mm, ac, cpu)
}

type fakeMemoryManager struct {
	platform.MemoryManager
}

func TestInspectOnEverySyscallBeforeReturn(t *testing.T) {
	wantCtx := gcontext.Background()
	wantMM := &fakeMemoryManager{}
	wantAC := &arch.Context64{}
	switches, inspections := 0, 0

	base := &fakePlatform{ctx: &fakeContext{switchToApp: func(ctx gcontext.Context, mm platform.MemoryManager, ac *arch.Context64, cpu int32) (*linux.SignalInfo, hostarch.AccessType, error) {
		if ctx != wantCtx || mm != wantMM || ac != wantAC || cpu != -1 {
			t.Fatal("Switch inputs were not forwarded")
		}
		switches++
		setTestSyscall(ac)
		return nil, hostarch.NoAccess, nil
	}}}
	wrapped := wrap(base, func(ctx gcontext.Context, mm platform.MemoryManager, ac *arch.Context64) {
		if ctx != wantCtx || mm != wantMM || ac != wantAC {
			t.Fatal("inspector did not receive the original context and memory manager")
		}
		if switches != inspections+1 {
			t.Fatal("inspector ran before the inner Switch returned")
		}
		args := ac.SyscallArgs()
		if ac.SyscallNo() != 257 || args[0].Uint64() != ^uint64(99) || args[1].Uint64() != 0x1234 || ac.IP() != 0x5678 {
			t.Fatalf("incorrect syscall registers: sysno=%d args=%v ip=%#x", ac.SyscallNo(), args, ac.IP())
		}
		inspections++
	})
	ctx := wrapped.NewContext(wantCtx)
	for n := 1; n <= 3; n++ {
		info, at, err := ctx.Switch(wantCtx, wantMM, wantAC, -1)
		if err != nil || info != nil || at != hostarch.NoAccess {
			t.Fatalf("Switch returned %v, %v, %v", info, at, err)
		}
		if inspections != n {
			t.Fatalf("Switch returned before inspection: got %d, want %d", inspections, n)
		}
	}
}

func TestInspectPassesNonSyscallEventsThrough(t *testing.T) {
	for _, eventErr := range []error{
		platform.ErrContextSignal,
		platform.ErrContextInterrupt,
		platform.ErrContextCPUPreempted,
		errors.New("platform failed"),
	} {
		t.Run(eventErr.Error(), func(t *testing.T) {
			info := &linux.SignalInfo{Signo: int32(linux.SIGSEGV)}
			base := &fakePlatform{ctx: &fakeContext{switchToApp: func(gcontext.Context, platform.MemoryManager, *arch.Context64, int32) (*linux.SignalInfo, hostarch.AccessType, error) {
				return info, hostarch.Read, eventErr
			}}}
			wrapped := wrap(base, func(gcontext.Context, platform.MemoryManager, *arch.Context64) {
				t.Fatal("non-syscall event reached inspector")
			})
			ctx := wrapped.NewContext(gcontext.Background())
			gotInfo, at, err := ctx.Switch(gcontext.Background(), nil, &arch.Context64{}, -1)
			if gotInfo != info || at != hostarch.Read || err != eventErr {
				t.Fatalf("event changed: %v, %v, %v", gotInfo, at, err)
			}
		})
	}
}

func TestWrapDisabled(t *testing.T) {
	base := &fakePlatform{}
	if wrap(base, nil) != base {
		t.Fatal("disabled inspector should return the original platform")
	}
}
