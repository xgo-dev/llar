//go:build linux && arm64

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
)

func main() {
	log.SetLevel(log.Warning)
	if len(os.Args) == 3 && os.Args[1] == "--capture-helper" {
		if err := capture(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	runtime.LockOSThread()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.WriteFile("/out/probe.failed", []byte(err.Error()), 0600)
		os.Exit(1)
	}
}

type artifact struct {
	Name string
	Data []byte
}
type buildContext struct {
	Count    int
	Metadata string
	Values   map[string]string
	Artifact *artifact
	Alias    *artifact
}

func run() error {
	ctx := &buildContext{Count: 41, Values: map[string]string{"before": "host"}}
	onBuild := func() {
		ctx.Count++
		ctx.Metadata = fmt.Sprintf("built-%d", ctx.Count)
		ctx.Values["during"] = "sentry"
		ctx.Artifact = &artifact{Name: "allocated in guest", Data: make([]byte, 8<<20)}
		ctx.Artifact.Data[0], ctx.Artifact.Data[len(ctx.Artifact.Data)-1] = 42, 99
		ctx.Alias = ctx.Artifact
	}
	mainTID := unix.Gettid()
	uid, gid := os.Getuid(), os.Getgid()
	start := make(chan struct{})
	ready := make(chan int, 2)
	finished := make(chan error, 2)
	for range 2 {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			tid := unix.Gettid()
			ready <- tid
			<-start
			if restored := unix.Gettid(); restored != tid {
				finished <- fmt.Errorf("worker TID changed: %d -> %d", tid, restored)
				return
			}
			finished <- nil
		}()
	}
	tids := []int{mainTID, <-ready, <-ready}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	defer w.Close()
	if err := r.SetReadDeadline(time.Now().Add(time.Hour)); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		PID         int     `json:"pid"`
		TIDs        []int   `json:"tids"`
		Pointer     uintptr `json:"pointer"`
		Initialized int64   `json:"initialized"`
	}{os.Getpid(), tids, uintptr(unsafe.Pointer(ctx)), time.Now().UnixNano()})
	if err != nil {
		return err
	}
	if err := os.WriteFile("/out/ready.json", data, 0644); err != nil {
		return err
	}
	fmt.Printf("caller: pid=%d heap=%d pointer=%p\n", os.Getpid(), ctx.Count, ctx)

	err = runSandbox(func() error {
		if restored := unix.Gettid(); restored != mainTID {
			return fmt.Errorf("main TID changed: %d -> %d", mainTID, restored)
		}
		if os.Getuid() != uid || os.Getgid() != gid {
			return fmt.Errorf("identity changed")
		}
		close(start)
		for range 2 {
			if err := <-finished; err != nil {
				return err
			}
		}
		runtime.GC()
		<-time.After(10 * time.Millisecond)
		go func() {
			time.Sleep(10 * time.Millisecond)
			_, err := w.Write([]byte("ok"))
			finished <- err
		}()
		buf := make([]byte, 2)
		if _, err := r.Read(buf); err != nil {
			return fmt.Errorf("epoll read: %w", err)
		}
		if err := <-finished; err != nil {
			return err
		}
		if string(buf) != "ok" {
			return fmt.Errorf("pipe data=%q", buf)
		}
		if _, err := os.ReadFile("/etc/hosts"); err != nil {
			return err
		}
		onBuild()
		fmt.Printf("guest: tids=%v pointer=%p count=%d metadata=%s GC/timer/epoll/file=ok\n", tids, ctx, ctx.Count, ctx.Metadata)
		return nil
	})
	if err != nil {
		return err
	}
	if ctx.Count != 42 || ctx.Metadata != "built-42" || ctx.Values["before"] != "host" || ctx.Values["during"] != "sentry" {
		return fmt.Errorf("context did not survive native restore: %+v", ctx)
	}
	if ctx.Artifact == nil || ctx.Alias != ctx.Artifact || len(ctx.Artifact.Data) != 8<<20 || ctx.Artifact.Data[0] != 42 || ctx.Artifact.Data[len(ctx.Artifact.Data)-1] != 99 {
		return fmt.Errorf("guest allocation/alias did not survive")
	}
	select {
	case <-start:
	default:
		return fmt.Errorf("channel state did not survive")
	}
	runtime.GC()
	<-time.After(10 * time.Millisecond)
	go func() {
		time.Sleep(10 * time.Millisecond)
		_, err := w.Write([]byte("rt"))
		finished <- err
	}()
	buf := make([]byte, 2)
	if _, err := r.Read(buf); err != nil {
		return fmt.Errorf("native epoll read: %w", err)
	}
	if err := <-finished; err != nil {
		return err
	}
	if string(buf) != "rt" {
		return fmt.Errorf("native pipe data=%q", buf)
	}
	var clearTID uintptr
	_, _, errno := unix.Syscall6(unix.SYS_PRCTL, unix.PR_GET_TID_ADDRESS, uintptr(unsafe.Pointer(&clearTID)), 0, 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("native-only prctl failed: %v", errno)
	}
	fmt.Printf("native: pid=%d pointer=%p count=%d metadata=%s map=%v allocation=%d alias=ok kernel=linux epoll=ok\n", os.Getpid(), ctx, ctx.Count, ctx.Metadata, ctx.Values, len(ctx.Artifact.Data))
	return os.WriteFile("/out/native.done", []byte("context restored\n"), 0600)
}
