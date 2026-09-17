//go:build linux && arm64

package sentryplatform

import "gvisor.dev/gvisor/pkg/sentry/arch"

func setTestSyscall(ac *arch.Context64) {
	ac.Regs.Regs[8] = 257
	ac.Regs.Regs[0] = ^uint64(99)
	ac.Regs.Regs[1] = 0x1234
	ac.Regs.Pc = 0x5678
}
