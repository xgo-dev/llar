//go:build linux

package sentryimport

import (
	"encoding/binary"
	"fmt"

	core "github.com/checkpoint-restore/go-criu/v8/crit/images/criu-core"
	sa "github.com/checkpoint-restore/go-criu/v8/crit/images/criu-sa"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/sentry/kernel"
)

func importThread(t *kernel.Task, c *core.CoreEntry, backend string) error {
	i := c.GetTiAarch64()
	r := i.GetGpregs()
	if len(r.Regs) != 31 {
		return fmt.Errorf("expected 31 ARM64 general registers")
	}
	ac := t.Arch()
	copy(ac.Regs.Regs[:], r.Regs)
	ac.Regs.Sp = r.GetSp()
	ac.Regs.Pc = r.GetPc()
	ac.Regs.Pstate = r.GetPstate()
	ac.SetTLS(uintptr(i.GetTls()))
	fp := *ac.FloatingPointData()
	state := i.GetFpsimd()
	if len(state.Vregs) != 64 {
		return fmt.Errorf("expected 32 SIMD registers")
	}
	if backend == "ptrace" {
		for j, value := range state.Vregs {
			binary.LittleEndian.PutUint64(fp[j*8:], value)
		}
		binary.LittleEndian.PutUint32(fp[512:], state.GetFpsr())
		binary.LittleEndian.PutUint32(fp[516:], state.GetFpcr())
	} else {
		binary.LittleEndian.PutUint32(fp, state.GetFpsr())
		binary.LittleEndian.PutUint32(fp[4:], state.GetFpcr())
		for j, value := range state.Vregs {
			binary.LittleEndian.PutUint64(fp[8+j*8:], value)
		}
	}
	t.SetClearTID(hostarch.Addr(i.GetClearTidAddr()))
	t.SetRobustList(hostarch.Addr(c.GetThreadCore().GetFutexRla()))
	t.SetSignalMask(linux.SignalSet(c.GetThreadCore().GetBlkSigset()))
	sas := c.GetThreadCore().GetSas()
	if sas != nil && !t.SetSignalStack(linux.SignalStack{Addr: sas.GetSsSp(), Size: sas.GetSsSize(), Flags: sas.GetSsFlags()}) {
		return fmt.Errorf("cannot restore signal stack")
	}
	return nil
}

func (s *checkpointImage) importSignals(tg *kernel.ThreadGroup) error {
	actions := s.threads[0].GetTc().GetSigactions()
	if len(actions) == 0 {
		var err error
		actions, err = decodeImage(s.dir, fmt.Sprintf("sigacts-%d.img", s.process.GetPid()), &sa.SaEntry{})
		if err != nil {
			return err
		}
	}
	i := 0
	for sig := linux.Signal(1); sig <= linux.Signal(64); sig++ {
		if sig == linux.SIGKILL || sig == linux.SIGSTOP {
			continue
		}
		if i >= len(actions) {
			return fmt.Errorf("short signal-action image")
		}
		a := actions[i]
		i++
		if _, err := tg.SetSigAction(sig, &linux.SigAction{Handler: a.GetSigaction(), Flags: a.GetFlags(), Restorer: a.GetRestorer(), Mask: linux.SignalSet(a.GetMask())}); err != nil {
			return err
		}
	}
	return nil
}
