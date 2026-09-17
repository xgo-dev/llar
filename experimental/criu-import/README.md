# CRIU ↔ Sentry 状态往返实验

测试程序在宿主创建 Go context 和闭包，进入 Sentry 执行，导出执行后的状态，再由 CRIU 恢复为原生 Linux 进程，从 `runSandbox` 后继续。context 没有按字段序列化成调用协议，也没有通过共享页回填业务结果。

这是原生 Linux ARM64、4 KiB 页、单次往返的实验。使用 gVisor ptrace platform；gVisor 源码、主 llar 模块和原有 `experimental/sentry-gate` 入口保持不变。尚未接入真实 ixgo formula。

## 调用语义

`cmd/probe/main_linux.go` 创建包含计数、字符串、map、指针和 slice 的 `buildContext`，调用：

```go
err := runSandbox(func() error {
    onBuild() // 在 Sentry 中修改 ctx，并分配新对象。
    return nil
})
// 此处回到原生 Linux，直接读取修改后的 ctx。
```

`runSandbox` 的业务返回值只有普通 Go `error`。没有之前的 `uint32` 结果 ABI；闭包捕获的 context 和回调局部状态通过进程快照保留。

## 已验证结果

2026-09-13，Linux `6.12.76-linuxkit`，CRIU `3.17.1`，Go `1.26.6`。依赖固定为 `go-criu/v8 v8.4.0` 和 gVisor Go-export `d1e35511e5a4`。

```text
caller: pid=7 heap=41 pointer=0x2f046556b530
guest: tids=[7 11 9] pointer=0x2f046556b530 count=42 metadata=built-42 GC/timer/epoll/file=ok
native: pid=7 pointer=0x2f046556b530 count=42 metadata=built-42 map=map[before:host during:sentry] allocation=8388608 alias=ok kernel=linux epoll=ok
```

验收包括：

- context 地址保持一致，计数在返回后为 42，字符串和 map 中保留 guest 的修改。
- guest 中新分配的 8 MiB slice 保留长度和内容，两个指针仍指向同一个新对象。
- guest 关闭的 Go channel 在返回后仍是关闭状态。
- 在 Sentry 内、原生返回后分别运行 GC、定时器和 Go netpoll 管道读写。
- 返回后执行 `PR_GET_TID_ADDRESS` 成功。当前 Sentry 对这个 prctl 返回 EINVAL，因此它同时验证后续代码已回到原生 Linux。
- 初始化记录没有重新生成；没有重新运行程序入口来重建 context。

一次记录从 8 个线程进入 Sentry，导出 11 个线程、28 段映射和 28,151,808 字节的私有页面。地址、线程数和页数随运行变化，不是性能指标。

## 进程和执行流程

```text
宿主程序 A 调用 runSandbox(fn)
  ↓ CRIU 保存入口快照；采集助手位于 A 的进程树之外
A 内调用 Sentry 库，恢复 guest B
  ↓ B 执行 fn，修改自己的整个 Go 状态
B 在返回边界等待；Sentry 暂停所有 guest Task
  ↓ 导出新的 CRIU 镜像
A 退出，采集/恢复助手继续工作
  ↓ CRIU 在独立 PID 命名空间中恢复原生进程 C
C 从 B 保存的返回边界继续，runSandbox 返回
  ↓ 后续代码直接访问被修改的 context
```

这里恢复的是新原生进程 C，原来的 A 不会用旧堆继续执行业务。新的 PID 命名空间避免 C 的线程 ID 与仍在运行的助手冲突。示例中 PID 都显示为 7，是各自命名空间内的编号，不代表保留了同一个宿主 OS 进程。

共享页只传递采集、guest 执行、返回请求和原生恢复等控制阶段，不承载 context 的业务返回数据。

`restore-init.py` 是新 PID 命名空间的单线程 PID 1：启动 CRIU，等待原生进程退出并返回其状态。验证脚本等待恢复助手完成；这不是生产 llar CLI 的生命周期实现。

## 实现位置

- `cmd/probe/main_linux.go`：context、闭包和往返验证。
- `cmd/probe/sandbox_linux.go`：Go 调用边界、CRIU 采集助手和原生恢复交接。
- `internal/sentryimport/run_linux.go`：同进程调用 Sentry；`RestoreAndExport` 在 guest 返回边界暂停、导出并退出 guest。
- `internal/sentryimport/export_linux.go`：生成更新后的 CRIU 线程、映射、页面、文件和 epoll 镜像。
- `internal/sentryimport/export_state_linux.go`：读取固定版本的 Sentry 状态对象，取得 TID、私有 PMA、FDTable 和 epoll 注册信息。
- `cmd/importer`：保留独立的单向转换/恢复诊断入口。

## 正向和反向转换

正向借用尚未执行的 `CreateProcess` 模板，建立有效的 TaskImage、syscall table 和 futex manager，再填入原地址上的页面、寄存器、TLS、FP、信号和 FD。通过 `pkg/state/wire` 重写 PID/TID 索引、缓存和时钟基准，随后走 `Kernel.LoadFrom`。

反向在 guest 完成回调后调用 `Pause` 和 `ReceiveTaskStates`。先通过 `SaveTo` 得到一致的 Sentry 状态，再重新生成 CRIU 的线程列表和 core、内存映射和 pagemap，以及执行后需要保存的页面。新增 Go 线程和匿名映射被纳入导出。

阻塞系统调用的寄存器按 Sentry 的 restart 状态转换为 Linux 可继续执行的状态。eventfd 的计数和 epoll 的监听集合、userdata 取自执行后的 Sentry 状态。普通 FD 的状态和位置也更新；未实现变化会被限制在实验边界内。

文件映射的可重建缓存通过 `MemoryManager.Invalidate` 清除，私有 COW 页面保留。procfs 展示的虚拟 vsyscall 行不是实际 VMA，不会导出成 ARM64 mmap。

Sentry 内使用同地址 VDSO syscall trampoline，让 Go 缓存的入口仍然有效。反向由 CRIU 处理原生 VDSO。此实验同机返回且 Sentry 单调时钟已与宿主对齐，所以不复制入口的 `timens-0.img`；原生恢复保留持续前进的宿主时钟。

这些操作都通过 gVisor 的现有库接口和状态编码完成，没有修改 gVisor 源码，没有用 linkname 或反射覆盖其私有字段。转换绑定当前状态布局，升级 gVisor 后必须重新验证。

## 复现

在本目录执行：

```sh
./verify.sh
```

需要 Go 1.26.3 或以上，以及原生 Linux ARM64 Docker 服务。产物保留在 `/tmp/llar-criu-verify.*`，包括入口 `images`、返回 `return-images`、各阶段日志和验证记录。

测试容器不联网，使用 `SYS_PTRACE`、`CHECKPOINT_RESTORE`、`SYS_ADMIN`、`NET_ADMIN`、`SYS_RESOURCE` 和关闭默认 seccomp 过滤。自采集程序以容器 root 运行，不是 rootless 验证。原生恢复助手建立独立 PID/mount namespace，并重新挂载 procfs。

## 当前边界

- 仅验证此单进程纯 Go probe 的一次往返；不支持任意 CRIU 镜像、任意 namespace 或重复调用。
- 支持测试所用的普通文件、共享文件映射、空管道、eventfd、epoll，以及同一进程中的新增线程和匿名内存。回调后的 FD 集合须保持不变；新资源、替换资源、非空管道和多进程导出尚未通用化。
- 没有通用处理排队信号、所有凭据/capability 细节、扩展 CPU 状态、网络连接、增量或远程页面。
- 原可执行文件及 backing files 必须保持不变。转换会复制页面并保存/加载完整状态，未做性能优化。
- systrap 仍因宿主高地址映射超出 `0xfe0000000000` 上界而拒绝导入。验证脚本检查拒绝路径，没有放宽地址保护。
- 启动代码沿用之前的 Sentry 实验，保留 Apache 版权头和 `LICENSE.gvisor`。
