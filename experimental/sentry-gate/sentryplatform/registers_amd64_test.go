//go:build linux && amd64

package sentryplatform

import "gvisor.dev/gvisor/pkg/sentry/arch"

func setTestSyscall(ac *arch.Context64) {
	ac.Regs.Orig_rax = 257
	ac.Regs.Rdi = ^uint64(99)
	ac.Regs.Rsi = 0x1234
	ac.Regs.Rip = 0x5678
}
