//go:build linux && arm64

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/goplus/llar/experimental/criu-import/internal/sentryimport"
	"golang.org/x/sys/unix"
)

const (
	waiting uint32 = iota
	captured
	executeGuest
	returnRequested
	exported
	nativeResumed
	captureFailed
)

// runSandbox invokes fn in Sentry and returns only in the native continuation
// restored from fn's completed state. The original supervisor exits after
// exporting that continuation. This experiment supports one round trip.
func runSandbox(fn func() error) error {
	f, err := os.OpenFile("/out/gate", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(4096); err != nil {
		return err
	}
	shared, err := unix.Mmap(int(f.Fd()), 0, 4096, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return err
	}
	defer unix.Munmap(shared)
	mode := (*uint32)(unsafe.Pointer(&shared[0]))
	request := (*uint32)(unsafe.Pointer(&shared[4]))
	if err := os.Mkdir("/out/images", 0700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "--capture-helper", strconv.Itoa(os.Getpid()))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// A sibling is outside CRIU's target process tree. A normal child would
	// make the checkpoint include its own capture helper.
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_PARENT}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start capture sibling: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		return err
	}
	// Release the process pidfd before allowing CRIU to inspect this process.
	atomic.StoreUint32(request, 1)
	for atomic.LoadUint32(mode) == waiting {
		time.Sleep(time.Millisecond)
	}
	if atomic.LoadUint32(mode) == captureFailed {
		return fmt.Errorf("CRIU capture failed; see /out/images/dump.log")
	}
	if atomic.LoadUint32(mode) == executeGuest {
		callbackErr := fn()
		atomic.StoreUint32(mode, returnRequested)
		for atomic.LoadUint32(mode) != nativeResumed {
			time.Sleep(time.Millisecond)
		}
		return callbackErr
	}

	if atomic.LoadUint32(mode) != captured {
		return fmt.Errorf("unexpected checkpoint phase %d", atomic.LoadUint32(mode))
	}
	fmt.Fprintf(os.Stderr, "caller pid=%d: converting and starting Sentry in this process\n", os.Getpid())
	if err := sentryimport.Run("/out/images", "/out/sentry.state", false, "ptrace"); err != nil {
		atomic.StoreUint32(mode, captureFailed)
		return err
	}
	atomic.StoreUint32(mode, executeGuest)
	if err := sentryimport.RestoreAndExport("/out/images", "/out/sentry.state", "ptrace", "/out/return-images", func() bool { return atomic.LoadUint32(mode) == returnRequested }); err != nil {
		atomic.StoreUint32(mode, captureFailed)
		return err
	}
	atomic.StoreUint32(mode, exported)
	// This supervisor still has the entry heap. Its continuation is now in
	// return-images and will resume as a native process through CRIU.
	os.Exit(0)
	return nil

}

func capture(pid string) error {
	f, err := os.OpenFile("/out/gate", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	shared, err := unix.Mmap(int(f.Fd()), 0, 4096, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return err
	}
	defer unix.Munmap(shared)
	mode := (*uint32)(unsafe.Pointer(&shared[0]))
	request := (*uint32)(unsafe.Pointer(&shared[4]))
	for atomic.LoadUint32(request) == 0 {
		time.Sleep(time.Millisecond)
	}
	cmd := exec.Command("criu", "dump", "-t", pid, "-D", "/out/images", "--shell-job", "--leave-running", "-v4", "-o", "dump.log")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		atomic.StoreUint32(mode, captureFailed)
		return err
	}
	data, err := os.ReadFile("/out/ready.json")
	if err == nil {
		err = os.WriteFile("/out/captured.json", data, 0600)
	}
	if err != nil {
		atomic.StoreUint32(mode, captureFailed)
		return err
	}
	atomic.StoreUint32(mode, captured)
	for atomic.LoadUint32(mode) != exported {
		if atomic.LoadUint32(mode) == captureFailed {
			return fmt.Errorf("Sentry export failed")
		}
		time.Sleep(time.Millisecond)
	}
	atomic.StoreUint32(mode, nativeResumed)
	restore := exec.Command("unshare", "--mount", "--pid", "--fork", "--mount-proc", "python3", "/experiment/restore-init.py")
	restore.Stdin, restore.Stdout, restore.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := restore.Run(); err != nil {
		_ = os.WriteFile("/out/restore.failed", []byte(err.Error()), 0600)
		return fmt.Errorf("native restore: %w", err)
	}
	return os.WriteFile("/out/restore.complete", []byte("ok\n"), 0600)
}
