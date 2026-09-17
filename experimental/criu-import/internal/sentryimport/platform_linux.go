//go:build linux && arm64

package sentryimport

import (
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/sentry/arch"
	"gvisor.dev/gvisor/pkg/sentry/platform"
)

type inspector func(context.Context, platform.MemoryManager, *arch.Context64)

type observedPlatform struct {
	platform.Platform
	inspect inspector
}

func (p *observedPlatform) NewContext(ctx context.Context) platform.Context {
	return &observedContext{Context: p.Platform.NewContext(ctx), inspect: p.inspect}
}

type observedContext struct {
	platform.Context
	inspect inspector
}

func (c *observedContext) Switch(ctx context.Context, mm platform.MemoryManager, ac *arch.Context64, cpu int32) (*linux.SignalInfo, hostarch.AccessType, error) {
	info, access, err := c.Context.Switch(ctx, mm, ac, cpu)
	if err == nil {
		ac.SyscallSaveOrig()
		c.inspect(ctx, mm, ac)
	}
	return info, access, err
}
