//go:build linux

// Package sentryplatform runs an inspector inside Sentry after Systrap returns
// a syscall and before the task goroutine executes it.
package sentryplatform

import (
	"gvisor.dev/gvisor/pkg/abi/linux"
	gcontext "gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/sentry/arch"
	"gvisor.dev/gvisor/pkg/sentry/platform"
	_ "gvisor.dev/gvisor/pkg/sentry/platform/systrap"
)

// Inspector runs synchronously on the Sentry task goroutine. Different tasks
// may call it concurrently. Arguments are borrowed for the duration of the call;
// inspect guest buffers through mm rather than dereferencing guest pointers.
type Inspector func(gcontext.Context, platform.MemoryManager, *arch.Context64)

// NewConstructor wraps Systrap with inspect. Register it under "systrap" before
// boot.New in the dedicated runtime so Loader forwards Systrap-specific options.
// A nil inspector preserves the original platform.
func NewConstructor(inspect Inspector) (platform.Constructor, error) {
	base, err := platform.Lookup("systrap")
	if err != nil {
		return nil, err
	}
	return &constructor{Constructor: base, inspect: inspect}, nil
}

type constructor struct {
	platform.Constructor
	inspect Inspector
}

func (c *constructor) New(opts platform.Options) (platform.Platform, error) {
	base, err := c.Constructor.New(opts)
	if err != nil {
		return nil, err
	}
	return wrap(base, c.inspect), nil
}

func wrap(base platform.Platform, inspect Inspector) platform.Platform {
	if inspect == nil {
		return base
	}
	return &wrappedPlatform{Platform: base, inspect: inspect}
}

type wrappedPlatform struct {
	platform.Platform
	inspect Inspector
}

func (p *wrappedPlatform) NewContext(ctx gcontext.Context) platform.Context {
	return &wrappedContext{
		Context: p.Platform.NewContext(ctx),
		inspect: p.inspect,
	}
}

type wrappedContext struct {
	platform.Context
	inspect Inspector
}

func (c *wrappedContext) Switch(ctx gcontext.Context, mm platform.MemoryManager, ac *arch.Context64, cpu int32) (*linux.SignalInfo, hostarch.AccessType, error) {
	info, at, err := c.Context.Switch(ctx, mm, ac, cpu)
	if err != nil {
		return info, at, err
	}

	// On arm64, SyscallArgs reads argument zero from OrigR0. The Sentry normally
	// fills OrigR0 immediately after Switch returns; doing it here first is
	// idempotent and lets both supported architectures use the same extraction.
	ac.SyscallSaveOrig()
	c.inspect(ctx, mm, ac)
	return info, at, nil
}
