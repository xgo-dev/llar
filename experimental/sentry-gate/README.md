# Sentry 上下文切换层实验

这个嵌套模块直接启动 Sentry，在同一个 Sentry 进程内同步执行 llar 拦截逻辑。ixgo 和 formula 运行在 guest 地址空间中。gVisor 通过官方 Go-export 模块引用，主 llar 模块不依赖 gVisor。

## 精简启动

cmd/sentry/boot_linux.go 和 fs_linux.go 从 gVisor 的 runsc/boot/loader.go、vfs.go、loader_test.go，以及 pkg/sentry/fsimpl/testutil/kernel.go 裁剪适配而来。对应 Go-export 提交为 d1e35511e5a41ee5c2afc522d2f9b0c27cf8d382；文件保留上游版权和修改来源，许可证见 LICENSE.gvisor。没有修改 gVisor 仓库或模块缓存中的源码。

```text
cmd/sentry
  → 初始化 Systrap + llar platform wrapper
  → 初始化 Kernel、MemoryFile、VDSO、时钟
  → 准备只读 DirectFS 根目录、guest procfs、stdio
  → Kernel.CreateProcess
  → Kernel.Start
  → Kernel.WaitExited
  → Kernel.Release
```

启动代码不再导入 runsc/boot、runsc/container 或 runsc/cli，不再创建 controller、启动同步管道、容器状态机，也没有 parent/gofer/monitor 子进程角色。guest 的 clone/exec 仍由 Sentry 内核正常处理。

DirectFS 初始化仍使用 LISAFS 协议取得根目录 FD。实验将服务放在 Sentry 内的 goroutine 中，继续使用 runsc/fsgofer，不启动独立 Gofer 进程。gVisor 自己的 stub/sysmsg 执行任务仍然存在。

Kernel.Init 必需的 namespace 和 cgroup2fs 单例保留；没有容器 cgroup 配置或管理操作。网络使用 Kernel.Init 的无网络默认值。这个程序只用于一次性的独立进程实验，初始化失败时退出进程。

## syscall 拦截

每个 guest Task 反复调用外层 Switch，外层再调用原始 Systrap Switch。内层返回 syscall 事件后直接调用 inspector；inspector 返回后，外层才返回，Sentry 随后执行 syscall。

```text
guest 发起 syscall
  → 原始 Systrap Switch 返回
  → llar inspector(ctx, mm, ac)，在同一个 Task goroutine 中执行
  → 外层 Switch 返回
  → Sentry 执行 syscall
  → 下一次 Switch 将执行结果交回 guest
```

拦截逻辑直接读取 ac；读取 guest 缓冲区时通过 mm 访问。不同 Task 的回调可以并发执行，回调不能保留传入的上下文指针。当前示例仅记录 syscall 号、参数和 IP，没有筛选 formula 线程，也没有记录 syscall 执行结果或提供策略 errno。signal、fault、interrupt 和平台错误原样返回。

没有为 llar 拦截器额外建立共享内存、请求队列或子进程。gVisor 内部的共享内存和同步机制继续使用原实现。

## 运行真实 formula

在本模块目录执行。以下命令适用于 ARM64 Docker 主机，Docker 仅提供 Linux 验证环境：

```sh
out=$(mktemp -d /tmp/llar-standalone-sentry.XXXXXX)
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly -o "$out/runner" ./cmd/sentry
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly -ldflags=-checklinkname=0 -o "$out/formula-probe" ./cmd/formula-probe
chmod 755 "$out" "$out/runner" "$out/formula-probe"
docker run --rm --platform linux/arm64 --user 1000:1000 --cap-drop ALL \
  --security-opt no-new-privileges --security-opt seccomp=unconfined \
  --network none --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,size=64m,mode=1777 \
  --mount "type=bind,source=$out,target=/demo,readonly" \
  debian:bookworm /demo/runner -probe=/demo/formula-probe
```

-root 默认为 /，可指定现有宿主目录作为 guest 的只读根目录；该目录需要包含 proc 挂载点。-probe 是这个 guest 根目录中的绝对可执行文件路径。

cmd/formula-probe 内嵌 formula/Probe_llar.gox，在 guest 内调用 llar 的 internal/formula.LoadFS，通过 ixgo 执行 OnBuild。formula 读取 /etc/hosts，将内容写入 BuildResult.Metadata，guest 检查结果包含 localhost。cmd/probe 是不使用 ixgo 的普通 Go guest。

这次 ARM64 运行得到：

```text
formula llar/sentry-formula-probe@1.0.0 ran through ixgo; metadata=148 bytes
Sentry pid=1: application exited successfully; observed=986 syscalls
```

日志中的 llar inspector PID 与 Sentry PID 一致；具体 syscall 数量随运行时调度变化。

## 验证边界

精简启动已通过 Linux ARM64 的普通 guest、真实 ixgo formula 和无效程序/根目录错误路径验证；AMD64、ARM64 编译及 go vet 通过。wrapper 单测覆盖重复 syscall、同步检查、非 syscall 事件透传和禁用回调。当前宿主为 ARM64；原生 AMD64 的 Sentry 端到端运行仍未验证。

这是实验启动器，没有安装 Sentry 宿主 seccomp 加固或建立完整生产运行环境。Gofer 与 Sentry 共进程，host 根目录由调用方选择；guest syscall 仍由 Systrap 拦截。生产 llar make 未接入。

此实验基于 goplus/llar main 的 bd473ea21632d57a4ed21e54144a55d0ee6c1872。formula 示例使用 main 的 OnBuild(ctx) 接口，错误通过 ctx.Errs、结果通过 ctx.Out 读取。嵌套模块仍固定使用 ixgo v1.1.7，并通过 replace github.com/goplus/llar => ../.. 引用当前 worktree；主模块的 go.mod 和依赖版本不受影响。
