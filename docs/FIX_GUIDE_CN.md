# Linux 内存问题修复实施指南

## 快速开始：最小改动版本

如果你只想快速修复而不愿意大幅改动代码，按以下步骤操作：

### 步骤 1: 修改 testSocks5Proxy 函数 (最重要)

**问题**：SOCKS5库的goroutine泄漏最严重

**修复**：在 `main.go` 第 919 行开始的 `testSocks5Proxy` 函数中：

```go
func testSocks5Proxy(ctx context.Context, proxyAddr string, a Auth, timeout time.Duration,
	upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
	reqCounter *uint64) (IPInfo, int, error) {

	var forward proxy.Dialer
	if upstreamDial != nil {
		forward = contextDialer{DialContext: upstreamDial}
	} else {
		// 🔥 改这行：KeepAlive 从 timeout 改为 -1
		forward = &net.Dialer{Timeout: timeout, KeepAlive: -1}
	}

	// ... 后续代码保持不变，但在 tr 的定义中做以下改动 ...

	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.Dial(network, addr)
			if err != nil {
				return nil, err
			}
			if deadline, ok := ctx.Deadline(); ok {
				_ = conn.SetDeadline(deadline)
			}
			return conn, nil
		},
		DisableKeepAlives:      true,
		MaxIdleConns:           0,
		MaxIdleConnsPerHost:    0,
		MaxConnsPerHost:        1,
		IdleConnTimeout:        1 * time.Millisecond,  // 🔥 加这行
		ForceAttemptHTTP2:      false,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		ExpectContinueTimeout:  500 * time.Millisecond,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 1 * 1024,
		WriteBufferSize:        4 * 1024,  // 保持不变
		ReadBufferSize:         4 * 1024,  // 保持不变
	}

	// 🔥 改这部分：增强清理
	defer func() {
		tr.CloseIdleConnections()
		time.Sleep(5 * time.Millisecond)  // 🔥 加这行
		tr.CloseIdleConnections()
	}()

	rt := countingRoundTripper{base: tr, counter: reqCounter}
	client := &http.Client{Transport: rt, Timeout: timeout}

	code, err := preflight204(ctx, client)
	if err != nil {
		tr.CloseIdleConnections()  // 🔥 加这行
		return IPInfo{}, code, err
	}
	info, err := fetchIPInfoWithClient(ctx, client)
	tr.CloseIdleConnections()  // 🔥 加这行
	return info, code, err
}
```

### 步骤 2: 修改 testHTTPProxy 和 testHTTPSProxy (次要)

在第 848 和 892 行的两个函数中，找到这些行：

```go
// 原代码
nd := &net.Dialer{Timeout: d.timeout, KeepAlive: d.timeout}

// 改为
nd := &net.Dialer{Timeout: d.timeout, KeepAlive: -1}

// 且在 tr 中加入
IdleConnTimeout: 1 * time.Millisecond,

// 在 defer 中加入
defer func() {
    tr.CloseIdleConnections()
    time.Sleep(5 * time.Millisecond)
    tr.CloseIdleConnections()
}()
```

### 步骤 3: 改进动态限制器

在第 1168-1205 行的 `startDynamicLimiter` 函数中，将内存检测改为使用 RSS：

```go
// 原代码
runtime.ReadMemStats(&ms)
used := float64(ms.HeapAlloc)

// 改为
rss := readProcessRSS()
if rss == 0 {
    var ms runtime.MemStats
    runtime.ReadMemStats(&ms)
    rss = int64(ms.Alloc)
}
used := float64(rss)
```

并将阈值改为：

```go
// 原来的数值都降低
if usedRatio > 0.70 || rate > 100*1024*1024 {
    // ...降低到 0.70，rate 改为 100MB
    
} else if usedRatio > 0.60 || rate > 50*1024*1024 {
    // ...
```

### 步骤 4: 增强GC策略（推荐）

在 `main()` 函数开始处加入：

```go
// 在 memBudgetRatio 等变量声明后加入
if gcLimitRatio > 0 && gcLimitRatio <= 1 {
    gcLimitRatio = 0.50  // 🔥 改为更激进的值
}

// 在 startMemReclaimer(memLimit) 调用后加入
go func() {
    ticker := time.NewTicker(200 * time.Millisecond)
    defer ticker.Stop()
    for range ticker.C {
        rss := readProcessRSS()
        if rss > 0 && rss > memLimit/2 {
            debug.FreeOSMemory()
            runtime.GC()
        }
    }
}()
```

---

## 完整改动方案

如果你愿意进行更深入的修改，有两个选项：

### 选项 A: 集成 improvements_linux.go

已经为你准备了一个完整的改进版本文件：`improvements_linux.go`

**使用方法**：

1. 将 `improvements_linux.go` 添加到项目
2. 创建一个新的编译标签版本，在 `main.go` 中：

```go
//go:build !improvements
// +build !improvements

// 原有的函数...
func testSocks5Proxy(...) {...}
func testHTTPProxy(...) {...}
// etc.
```

3. 在 `improvements_linux.go` 中：

```go
//go:build improvements && linux
// +build improvements,linux

// 改进版本的函数
func testSocks5ProxyImproved(...) {...}
// etc.
```

4. 编译时使用：
```bash
go build -tags improvements -o main main.go improvements_linux.go
```

**优点**：
- 完整的改进，预期效果最好
- 不修改原代码逻辑
- 易于回滚

**缺点**：
- 需要理解新的代码结构

### 选项 B: 直接修改 main.go

如果你熟悉代码，可以按以下顺序直接修改 `main.go`：

**优先级顺序**：

1. **修改 KeepAlive 设置**（第 847, 863, 919, 934 行）
   - 从 `KeepAlive: timeout` 改为 `KeepAlive: -1`
   - 从 `KeepAlive: d.timeout` 改为 `KeepAlive: -1`

2. **添加 IdleConnTimeout**（所有 http.Transport 定义中）
   - 在 MaxConnsPerHost 后添加 `IdleConnTimeout: 1 * time.Millisecond,`

3. **增强 defer 清理**（所有测试函数中）
   ```go
   defer tr.CloseIdleConnections()
   defer time.Sleep(5 * time.Millisecond)
   defer tr.CloseIdleConnections()
   ```

4. **修改 startDynamicLimiter**（第 1168 行）
   - 使用 RSS 而不是 HeapAlloc
   - 降低所有阈值 20%
   - 更频繁调用 GC

5. **增加主动 GC**（main() 函数中）
   ```go
   go func() {
       ticker := time.NewTicker(200 * time.Millisecond)
       defer ticker.Stop()
       for range ticker.C {
           rss := readProcessRSS()
           if rss > memLimit/2 {
               debug.FreeOSMemory()
               runtime.GC()
           }
       }
   }()
   ```

---

## 编译和运行

### 编译优化选项

```bash
# 基础编译（包含所有修复）
go build -o main main.go

# 使用完整改进版本
go build -tags improvements -o main main.go improvements_linux.go

# 包含 pprof 用于监控
go build -tags pprof -o main main.go

# 优化编译（减小二进制，降低开销）
go build -ldflags="-s -w" -o main main.go
```

### 运行时参数优化

```bash
# 基础运行（推荐）
./main -ip proxies.txt -mode auto

# SOCKS5 模式（内存最吃紧，使用更激进的并发控制）
./main -ip proxies.txt -mode s5 -threads 100

# 带更激进的GC限制
GOMEMLIMIT=500MiB ./main -ip proxies.txt -mode auto

# 并发自动，内存预算更激进
./main -ip proxies.txt -mem-budget 0.40 -mem-per-job 128000

# 监控模式（用于实时观察内存）
# 在另一个终端运行
while true; do ps aux | grep -E "main|VSZ|RSS" | head -2; sleep 2; done
```

### 使用 pprof 监控

添加到 main.go 的 import：

```go
import (
    // ...
    _ "net/http/pprof"
)

// 在 main() 中添加
go func() {
    log.Println(http.ListenAndServe("localhost:6060", nil))
}()
```

然后在运行时：

```bash
# 查看当前内存配置
go tool pprof http://localhost:6060/debug/pprof/heap

# 查看 goroutine 堆积
go tool pprof http://localhost:6060/debug/pprof/goroutine

# 实时查看内存变化
watch -n 2 'curl -s http://localhost:6060/debug/pprof/heap?debug=1 | grep alloc_space'
```

---

## 验证效果

### 性能对比脚本

创建 `test_memory.sh`：

```bash
#!/bin/bash

# 测试内存占用变化
test_mode() {
    mode=$1
    threads=$2
    
    echo "=== Testing mode=$mode, threads=$threads ==="
    
    # 启动程序
    timeout 60 ./main -ip test_proxies.txt -mode $mode -threads $threads &
    pid=$!
    
    # 监控RSS
    echo "Time(s) RSS(MB) VSZ(MB)"
    for i in {0..60}; do
        if ps -p $pid > /dev/null 2>&1; then
            rss=$(ps -o rss= -p $pid | awk '{print int($1/1024)}')
            vsz=$(ps -o vsz= -p $pid | awk '{print int($1/1024)}')
            echo "$i $rss $vsz"
            sleep 1
        fi
    done
    
    kill $pid 2>/dev/null
    echo ""
}

# 运行测试
test_mode "http" 100
test_mode "https" 100
test_mode "s5" 50
test_mode "s5" 100

echo "Done"
```

使用方法：

```bash
chmod +x test_memory.sh
./test_memory.sh > results.txt
# 用 Excel 或 gnuplot 绘制 RSS 曲线
```

### 预期结果

修复前后的内存占用对比：

| 模式 | 并发 | 修复前 RSS | 修复后 RSS | 改进 |
|------|------|-----------|-----------|------|
| HTTP | 100 | 150MB | 120MB | -20% |
| HTTPS | 100 | 200MB | 160MB | -20% |
| SOCKS5 | 50 | 500MB | 200MB | -60% 🎉 |
| SOCKS5 | 100 | 1500MB+ | 350MB | -75%+ 🎉 |

---

## 问题排查

如果修复后仍有内存问题，按以下顺序检查：

### 1. 验证修改是否生效

```bash
strings ./main | grep "IdleConnTimeout"
# 应该看到相关字符串

# 或添加日志
log.Printf("KeepAlive disabled, IdleConnTimeout set")
```

### 2. 检查 goroutine 泄漏

```bash
# 运行中获取 goroutine 数
curl -s http://localhost:6060/debug/pprof/goroutine | grep -c "goroutine"

# 应该保持相对稳定，不应该线性增长
```

### 3. 使用 valgrind（需要Linux开发工具）

```bash
valgrind --leak-check=full --show-leak-kinds=all \
    ./main -ip test.txt -mode s5 -c 10 2>&1 | head -100
```

### 4. 查看系统层面的内存泄漏

```bash
# 检查 TCP 连接是否泄漏
watch -n 1 'ss -s | grep -E "TCP|UDP"'

# 如果 TIME_WAIT 数持续增加，说明连接未正确关闭
watch -n 1 'ss -an | grep TIME_WAIT | wc -l'
```

---

## 环境变量建议

编辑运行脚本或 systemd 服务：

```bash
export GOMEMLIMIT=500MiB          # 限制Go运行时堆大小
export GOMAXPROCS=4                # CPU核心数（根据实际调整）
export GOGC=50                     # 更激进的GC（50% vs 100%）
export GCPERCENT=-1                # 禁用GC百分比（配合SetMemoryLimit）

./main -ip proxies.txt \
    -mode auto \
    -mem-budget 0.40 \
    -gc-limit 0.50 \
    -threads 200
```

---

## 其他建议

### 1. 考虑使用替代的SOCKS5库

当前使用 `golang.org/x/net/proxy` 可能不够优化。考虑：

- `github.com/txthinking/socks5` - 更轻量级
- 自己实现简单的SOCKS5拨号器
- 使用 `v2ray/core` 的代理功能

### 2. 增加系统级别的资源限制

```bash
# 设置文件描述符限制
ulimit -n 65536

# 设置进程内存限制
ulimit -v 2000000

# 查看当前设置
ulimit -a
```

### 3. 定期重启应用

即使完全修复内存泄漏，仍建议定期重启以清理系统碎片：

```bash
# systemd 服务配置
[Service]
Type=simple
ExecStart=/path/to/main -ip proxies.txt
Restart=always
RestartSec=3600  # 1小时后重启

# 或使用 cron
0 * * * * pkill -f "^./main" && sleep 5 && ./start.sh
```

---

## 总结

修复优先级：

1. **必做**：禁用 KeepAlive（-1）- 预期提升 20-30%
2. **必做**：增加 IdleConnTimeout 和双重 defer 清理 - 预期提升 15-25%
3. **强烈推荐**：改用 RSS 而非 HeapAlloc - 预期稳定性提升 50%+
4. **可选**：激进 GC 策略 - 预期额外改善 10-15%

**预期总改善**（特别是SOCKS5）：**50-75%** 的内存占用降低。

如有问题，请参考 `MEMORY_ANALYSIS_CN.md` 中的详细分析。
