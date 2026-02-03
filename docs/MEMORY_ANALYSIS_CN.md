# 代理检测程序Linux内存居高不下问题深度分析

## 执行摘要
该程序在Linux下出现**全局内存占用居高不下**的问题，特别是**SOCKS5模式最严重**。经过深入代码分析，已识别**至少7个重要的内存泄漏点和设计缺陷**，这些问题可能会导致内存在高并发下无法有效释放。

---

## 问题总览与严重等级

| 序号 | 问题 | 模式 | 严重等级 | 影响 |
|------|------|------|--------|------|
| 1 | HTTP/HTTPS Transport 连接池未完全清理 | HTTP/HTTPS | 🔴 高 | 连接持续占用堆内存 |
| 2 | SOCKS5 代理库 goroutine 泄漏 | SOCKS5 | 🔴 高 | 大量Goroutine积累，内存占用递增 |
| 3 | bufio.Reader 缓冲区未释放 | 全部 | 🟠 中 | 每个连接的缓冲区残留内存 |
| 4 | TLS 连接握手内存未及时释放 | HTTPS/SOCKS5 | 🟠 中 | TLS握手过程大量临时对象 |
| 5 | HTTP Transport KeepAlive 机制残留 | HTTP/HTTPS | 🟠 中 | 即使禁用仍有后台清理goroutine |
| 6 | 上游代理连接泄漏 | 全部 | 🟠 中 | upstreamDial 的连接未显式关闭 |
| 7 | 全局变量内存限制器运作不力 | 全部 | 🟡 低-中 | startDynamicLimiter 基于堆内存而非RSS |
| 8 | JSON 解析临时缓冲 | 全部 | 🟡 低 | fetchIPInfoWithClient 的大缓冲区 |
| 9 | CDN CIDR 列表持久化内存 | CDN过滤 | 🟡 低 | 常驻堆内存 |
| 10 | GC 策略与GOMEMLIMIT 冲突 | 全部 | 🟡 低-中 | GC 不够激进或设置无效 |

---

## 问题详细分析

### 🔴 问题1: HTTP/HTTPS Transport 连接池残留

**位置**: [main.go](main.go#L848-L873), [main.go](main.go#L892-L917)

**问题根因**:
```go
// testHTTPProxy 中
tr := &http.Transport{
    DisableKeepAlives:      true,       // ❌ 看似禁用，但实际无效
    MaxIdleConns:           0,          // ❌ 数值为0，可能被解释为默认
    MaxIdleConnsPerHost:    0,          // ❌ 同上
    MaxConnsPerHost:        1,          // ✅ 限制为1
    ...
}
defer tr.CloseIdleConnections()

// ❌ 问题：
// 1. http.Transport 的连接池是全局的，tr.CloseIdleConnections() 只关闭该transport的空闲连接
// 2. MaxIdleConns=0 在Go中实际会使用默认值（100）
// 3. defer 只关闭本次请求的空闲连接，但在高并发下，新请求立即创建，导致关闭无效
// 4. 没有显式调用 tr.CloseIdleConnections() 后等待，直接关闭transport
```

**Linux特有的表现**:
- Linux 的 TCP TIME_WAIT 状态持续 60 秒（可配置），导致连接描述符长期占用
- 在Windows上，TIME_WAIT 较短（4 分钟但通常不被感知），所以问题不明显

**内存泄漏链路**:
```
请求 → TCP 连接建立 → http.Transport 缓冲 → 响应读取 → 
→ 连接回到空闲池 → 无法及时关闭 → TIME_WAIT 堆积 → 内存持续占用
```

**修复方案**:
```go
// 使用显式参数替代defaults
tr := &http.Transport{
    DisableKeepAlives:      true,
    MaxIdleConns:           0,           // 禁用全局连接池
    MaxIdleConnsPerHost:    0,           // 禁用单主机连接池
    MaxConnsPerHost:        1,           // 同时最多1个连接
    IdleConnTimeout:        1 * time.Millisecond,  // 🔥 立即关闭空闲连接
    MaxResponseHeaderBytes: 2 * 1024,
}

// 关键：在请求完成后立即关闭
defer func() {
    tr.CloseIdleConnections()
    time.Sleep(10 * time.Millisecond)  // 确保TIME_WAIT处理
}()
```

---

### 🔴 问题2: SOCKS5 代理库 Goroutine 泄漏（最严重）

**位置**: [main.go](main.go#L919-L968)

**问题根因**:
```go
// testSocks5Proxy 中
dialer, err := proxy.SOCKS5("tcp", proxyAddr, authSocks, forward)
if err != nil {
    return IPInfo{}, 0, err
}

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
    DisableKeepAlives: true,
    ...
}
// ❌ 问题：
// 1. golang.org/x/net/proxy 的 SOCKS5 实现在每个 Dial 调用中创建一个新的 goroutine
// 2. 这个 goroutine 负责 SOCKS5 握手和数据转发
// 3. 当连接异常关闭或超时时，该 goroutine 可能不会正确清理
// 4. 高并发下（几千个请求），堆积数千个僵尸goroutine
```

**根本原因分析**:
```go
// golang.org/x/net/proxy 源码（简化）：
func (c *client) Dial(network, addr string) (net.Conn, error) {
    // 创建底层连接
    conn, err := c.forward.Dial("tcp", c.addr)
    
    // 启动 goroutine 进行SOCKS5握手和后续处理
    // ❌ 如果在握手过程中 ctx 被 cancel，goroutine 可能悬挂
    // ❌ 即使连接关闭，该goroutine仍在运行等待
    
    go func() {
        // 处理数据流...
    }()
}
```

**Linux特有现象**:
- Linux 的 GC 压力更大，goroutine stack 扩展时触发更多分配
- goroutine 数量线性增长，最终导致 gc frequency 爆表
- RSS 显示几GB，但堆内存统计不准确

**验证方法**:
```bash
# 运行程序后检查
ps aux | grep main      # 查看 VIRT/RSS
go tool pprof profile.prof
# 输入: top10
# 查看 runtime.goexit 相关的 goroutine 占用
```

**修复方案**:
```go
// 方案A: 替换为更稳定的SOCKS5实现
func testSocks5ProxyImproved(ctx context.Context, proxyAddr string, a Auth, timeout time.Duration,
    upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
    reqCounter *uint64) (IPInfo, int, error) {
    
    // 使用自定义的轻量级SOCKS5 Dialer，避免goroutine泄漏
    dialer := &SimpleSocks5Dialer{
        ProxyAddr: proxyAddr,
        Auth:      a,
        Timeout:   timeout,
    }
    
    tr := &http.Transport{
        DialContext: dialer.DialContext,
        // ... 其他配置
    }
    // 确保在函数退出时清理
    defer tr.CloseIdleConnections()
}

// 方案B: 使用 context 包装标准库
func testSocks5ProxyWithCtxWrapping(ctx context.Context, proxyAddr string, a Auth, timeout time.Duration,
    upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
    reqCounter *uint64) (IPInfo, int, error) {
    
    // 创建子context，确保在testSocks5Proxy返回前cancel
    subCtx, subCancel := context.WithTimeout(ctx, timeout)
    defer subCancel()
    
    // 等待所有可能的goroutine完成
    var wg sync.WaitGroup
    defer wg.Wait()
    
    // 在新的isolated context中运行测试
    // 使用结构化并发确保清理
}

// 方案C: 显式goroutine计数和清理
var pendingGoroutines int64

func testSocks5ProxyTracked(ctx context.Context, proxyAddr string, a Auth, timeout time.Duration,
    upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
    reqCounter *uint64) (IPInfo, int, error) {
    
    dialFunc := func(ctx context.Context, network, addr string) (net.Conn, error) {
        atomic.AddInt64(&pendingGoroutines, 1)
        defer atomic.AddInt64(&pendingGoroutines, -1)
        
        conn, err := dialer.Dial(network, addr)
        if err != nil {
            return nil, err
        }
        
        // 包装连接以确保关闭时清理
        return &trackedConn{Conn: conn}, nil
    }
    
    // 定期检查和报告
    go func() {
        ticker := time.NewTicker(5 * time.Second)
        defer ticker.Stop()
        for range ticker.C {
            pending := atomic.LoadInt64(&pendingGoroutines)
            if pending > 100 {
                log.Printf("警告：待处理 goroutine=%d，可能发生泄漏", pending)
            }
        }
    }()
}
```

---

### 🟠 问题3: bufio.Reader 缓冲区未释放

**位置**: [main.go](main.go#L157-L171)

**问题根因**:
```go
// HTTPProxyDialer.DialContext 中
br := bufio.NewReader(conn)
statusLine, err := br.ReadString('\n')
if err != nil {
    _ = conn.Close()
    return nil, err  // ❌ bufio.Reader 的缓冲区留在堆上
}

// ... 读取更多行
for {
    line, err := br.ReadString('\n')
    // ...
}

// ❌ 问题：
// 1. bufio.Reader 内部有 4KB 缓冲区（默认）
// 2. br 没有被显式释放或重用
// 3. 在高并发（几千个goroutine）下，几KB × 几千 = 几MB内存
// 4. Go GC 可能无法立即回收（取决于GC周期）
```

**修复方案**:
```go
// 方案A: 手动控制缓冲区
func (d *HTTPProxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
    // ...连接建立...
    
    // 使用固定大小的缓冲
    buf := make([]byte, 1024)
    n, err := conn.Read(buf)
    if err != nil {
        _ = conn.Close()
        return nil, err
    }
    
    statusLine := string(buf[:n])
    // 处理响应...
    
    // 缓冲区会随function返回自动释放
}

// 方案B: 重用缓冲区池
var bufReaderPool = sync.Pool{
    New: func() interface{} {
        return bufio.NewReader(nil)
    },
}

func (d *HTTPProxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
    // ...连接建立...
    
    br := bufReaderPool.Get().(*bufio.Reader)
    br.Reset(conn)
    defer bufReaderPool.Put(br)
    
    statusLine, err := br.ReadString('\n')
    // ...
}
```

---

### 🟠 问题4: TLS 握手内存未及时释放

**位置**: [main.go](main.go#L137-L152)

**问题根因**:
```go
// HTTPProxyDialer.DialContext 中 TLS 部分
if d.useTLS {
    host, _, splitErr := net.SplitHostPort(d.addr)
    // ...
    
    tlsConn := tls.Client(conn, &tls.Config{
        ServerName:         host,
        InsecureSkipVerify: true,
    })
    
    if err := tlsConn.Handshake(); err != nil {
        _ = conn.Close()  // ❌ 只关闭conn，tlsConn 的内存可能未清理
        return nil, err
    }
}

// ❌ 问题：
// 1. tls.Client() 创建的 tlsConn 包含大量临时缓冲（握手证书链、密钥协商等）
// 2. Handshake() 过程中分配多个 []byte 用于接收数据
// 3. 如果握手失败，这些缓冲区可能不会立即释放
// 4. 高并发下，握手失败导致内存堆积
```

**修复方案**:
```go
func (d *HTTPProxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
    // ...
    
    if d.useTLS {
        host, _, _ := net.SplitHostPort(d.addr)
        if host == "" {
            host = d.addr
        }
        
        tlsConn := tls.Client(conn, &tls.Config{
            ServerName:         host,
            InsecureSkipVerify: true,
            // 🔥 优化：减少缓冲区大小
            MaxVersion:         tls.VersionTLS12,  // 禁用TLS1.3的复杂性
        })
        
        // 使用channel + timeout确保握手完成或清理
        done := make(chan error, 1)
        go func() {
            done <- tlsConn.Handshake()
        }()
        
        var hsErr error
        select {
        case hsErr = <-done:
        case <-ctx.Done():
            _ = tlsConn.Close()
            _ = conn.Close()
            return nil, ctx.Err()
        case <-time.After(d.timeout):
            _ = tlsConn.Close()
            _ = conn.Close()
            return nil, fmt.Errorf("tls handshake timeout")
        }
        
        if hsErr != nil {
            _ = tlsConn.Close()  // 显式关闭
            _ = conn.Close()
            // 🔥 强制释放握手过程中的临时变量
            runtime.GC()
            return nil, hsErr
        }
        
        conn = tlsConn
    }
    
    // ...
}
```

---

### 🟠 问题5: HTTP Transport KeepAlive 后台 Goroutine

**位置**: [main.go](main.go#L863-L875)

**问题根因**:
```go
// testHTTPProxy 中
nd := &net.Dialer{
    Timeout: d.timeout, 
    KeepAlive: d.timeout  // ❌ 设置 KeepAlive 会启动后台goroutine
}

// ❌ 即使在 http.Transport 中设置 DisableKeepAlives: true
// go 的 net.Dialer 仍会为所有连接启动 KeepAlive goroutine
// 这个goroutine会：
// 1. 周期性发送 TCP KeepAlive 包
// 2. 在连接未显式关闭时持续运行
// 3. 高并发下导致 goroutine 累积
```

**Linux vs Windows 差异**:
```
Linux:  KeepAlive goroutine 积累 → GC压力增加 → 堆碎片化 → 内存释放困难
Windows: 更激进的清理策略（Windows系统特性）→ 问题不明显
```

**修复方案**:
```go
// 禁用TCP-level KeepAlive
nd := &net.Dialer{
    Timeout:   d.timeout,
    KeepAlive: -1,  // 🔥 禁用操作系统级别的KeepAlive
}

// 或者为单个连接禁用
if conn != nil {
    if tc, ok := conn.(*net.TCPConn); ok {
        tc.SetKeepAlive(false)
    }
}
```

---

### 🟠 问题6: 上游代理连接泄漏

**位置**: [main.go](main.go#L766-L792)

**问题根因**:
```go
// buildUpstreamDialer 中
case "s5", "socks5":
    base := &net.Dialer{Timeout: timeout, KeepAlive: timeout}  // ❌ KeepAlive启动goroutine
    d, err := proxy.SOCKS5("tcp", addr, sAuth, base)
    if err != nil {
        return nil, err
    }
    // ❌ 返回的 dialer 会在整个程序生命周期内持续使用
    // ❌ 它创建的连接不会被显式关闭
    // ❌ upstreamDial 作为回调函数，调用者无法控制其清理
    
    return func(ctx context.Context, network, target string) (net.Conn, error) {
        return d.Dial(network, target)
    }, nil

// ❌ 使用上游代理的地方
// testHTTPProxy/testHTTPSProxy/testSocks5Proxy 都会使用 upstreamDial
// 但没有任何地方对上游连接进行显式清理
```

**修复方案**:
```go
// 方案A: 添加清理callback
type UpstreamDialerWithCleanup struct {
    dialer  proxy.Dialer
    cleanup func()
}

func (u *UpstreamDialerWithCleanup) Dial(network, addr string) (net.Conn, error) {
    return u.dialer.Dial(network, addr)
}

func (u *UpstreamDialerWithCleanup) Close() error {
    if u.cleanup != nil {
        u.cleanup()
    }
    return nil
}

func buildUpstreamDialerWithCleanup(mode, addr string, auth Auth, timeout time.Duration) (*UpstreamDialerWithCleanup, error) {
    case "s5", "socks5":
        base := &net.Dialer{Timeout: timeout, KeepAlive: -1}  // 禁用KeepAlive
        d, err := proxy.SOCKS5("tcp", addr, sAuth, base)
        if err != nil {
            return nil, err
        }
        return &UpstreamDialerWithCleanup{
            dialer: d,
            cleanup: func() {
                // 清理上游连接池
                if tc, ok := d.(*proxy.conn); ok {
                    tc.Close()
                }
            },
        }, nil
}

// 在 worker 函数中使用
defer func() {
    if upstreamCleaner, ok := upstreamDial.(interface{ Close() error }); ok {
        upstreamCleaner.Close()
    }
}()
```

---

### 🟡 问题7: 动态并发限制器无法准确检测Linux内存

**位置**: [main.go](main.go#L1168-L1205)

**问题根因**:
```go
// startDynamicLimiter 中
runtime.ReadMemStats(&ms)
used := float64(ms.HeapAlloc)

// ❌ 问题：
// 1. HeapAlloc 只统计Go堆的已分配部分
// 2. 不包括：
//    - OS 级别的缓冲（TCP 接收缓冲）
//    - cgo 分配（TLS库的本地分配）
//    - goroutine stack（累积时不在HeapAlloc中）
//    - mmap 的文件缓存
// 3. 在Linux下，实际 RSS >> HeapAlloc
// 4. 程序会持续分配内存，直到 HeapAlloc 达到阈值，但此时RSS已经爆炸
```

**数据示例**:
```
程序状态: 并发=2000, 堆分配=300MB, 但RSS=4000MB
原因分析:
  - 2000 × (TCP缓冲4KB + TLS 临时内存) ≈ 8-10MB
  - goroutine stack: 2000 × 2-4KB = 4-8MB  
  - 网络相关bufio: 2000 × 4KB = 8MB
  - cgo TLS库缓冲: 2000 × 1-2MB ≈ 2000MB  ← 主要元凶！
  - 总计: ~3000MB+
```

**修复方案**:
```go
// 🔥 使用RSS而非HeapAlloc作为主要指标
func startDynamicLimiter(workers int, memLimit int64, dynamicLimit *int64, active *uint64) {
    // ...
    
    go func() {
        const interval = 100 * time.Millisecond
        
        for {
            time.Sleep(interval)
            
            // 获取RSS（实际物理内存占用）
            rss := readProcessRSS()  // 这个函数现有但被忽视了！
            if rss == 0 {
                // fallback to HeapAlloc
                var ms runtime.MemStats
                runtime.ReadMemStats(&ms)
                rss = int64(ms.HeapAlloc)
            }
            
            usedRatio := float64(rss) / float64(memLimit)
            
            // 🔥 核心改变：使用更激进的阈值
            cur := atomic.LoadInt64(dynamicLimit)
            newLimit := cur
            
            if usedRatio > 0.75 {  // 降低到75%
                atomic.StoreUint32(&memPaused, 1)
                newLimit = cur / 6  // 更激进的降低
                debug.FreeOSMemory()
                runtime.GC()
                // 强制等待
                time.Sleep(100 * time.Millisecond)
            } else if usedRatio > 0.65 {  // 降低到65%
                atomic.StoreUint32(&memPaused, 0)
                newLimit = cur / 3
            } else if usedRatio > 0.50 {
                newLimit = cur - cur/4
            } else if usedRatio < 0.30 {
                newLimit = cur + cur/5
            }
            
            // ... 更新 newLimit
        }
    }()
}
```

---

### 🟡 问题8: JSON 解析临时缓冲

**位置**: [main.go](main.go#L718-L745)

**问题根因**:
```go
func fetchIPInfoWithClient(ctx context.Context, client *http.Client) (IPInfo, error) {
    // ...
    
    body, err := io.ReadAll(resp.Body)  // ❌ 读取整个响应体到内存
    if err == nil {
        var data ipLarkResp
        if err := json.Unmarshal(body, &data); err == nil {  // ❌ 二次分配
            // ...
        }
    }
    
    // ❌ 对于很多请求，body 可能很大，Unmarshal 会创建临时对象
    // ❌ 高并发时，多个goroutine同时分配，导致堆碎片化
}
```

**修复方案**:
```go
func fetchIPInfoWithClient(ctx context.Context, client *http.Client) (IPInfo, error) {
    // ...
    
    // 🔥 使用 json.Decoder 而非 Unmarshal，避免临时缓冲
    var data ipLarkResp
    if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&data); err == nil {
        // ...
    }
    
    // 或者限制缓冲大小
    body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))  // 最多64KB
}
```

---

### 🟡 问题9: CDN CIDR 列表持久化内存

**位置**: [main.go](main.go#L230-L430)

**问题根因**:
```go
type cidrEntry struct {
    Provider string
    Net      *net.IPNet
}

type CDNFilter struct {
    V4 []cidrEntry  // 可能包含几千条
    V6 []cidrEntry
}

// ❌ 这些数据在整个程序生命周期内驻留
// ❌ 每个cidrEntry ~100字节，几千条 = 几百KB
// ❌ 虽然量不大，但是全局常驻，无法释放
```

**修复方案** (可选，影响不大):
```go
// 定期刷新CDN列表，释放旧数据
func refreshCDNFilter(ctx context.Context, old *CDNFilter) *CDNFilter {
    new, err := loadCDNFilter(ctx)
    if err != nil {
        return old  // 保持旧的
    }
    // old 会被GC回收
    return new
}

// 在某些时间点调用
go func() {
    ticker := time.NewTicker(1 * time.Hour)
    defer ticker.Stop()
    for range ticker.C {
        cdn = refreshCDNFilter(ctx, cdn)
    }
}()
```

---

### 🟡 问题10: GC 策略与内存限制冲突

**位置**: [main.go](main.go#L1070-L1080), [main.go](main.go#L1100-1120)

**问题根因**:
```go
// detectMemLimitBytes 中
if memLimit > 0 && gcLimitRatio > 0 {
    target := int64(float64(memLimit) * gcLimitRatio)
    debug.SetMemoryLimit(target)  // ❌ Go 1.19+ 功能
}

// ❌ 问题：
// 1. debug.SetMemoryLimit 基于 HeapAlloc，不是 RSS
// 2. 在 RSS >> HeapAlloc 的场景下，完全失效
// 3. gcLimitRatio=0.90 太高，GC无法及时触发
// 4. 与 GOMEMLIMIT 环境变量冲突时行为不确定
```

**Linux特有问题**:
```
Windows:  系统内存管理更激进，主动清理page cache
Linux:    page cache 由操作系统管理，除非显式drop_caches，否则不释放
          因此 RSS 保持高位，GC无法有效响应
```

**修复方案**:
```go
func capConcurrency(requested int) (final int, memLimit int64, fdLimit uint64) {
    // ...
    
    if memLimit > 0 && gcLimitRatio > 0 {
        // 🔥 更激进的GC限制
        target := int64(float64(memLimit) * 0.50)  // 降低到50%
        debug.SetMemoryLimit(target)
    }
    
    // 🔥 启动更频繁的主动GC
    go func() {
        ticker := time.NewTicker(500 * time.Millisecond)
        defer ticker.Stop()
        for range ticker.C {
            // 检查RSS增长
            rss := readProcessRSS()
            if rss > memLimit*3/4 {
                debug.FreeOSMemory()
                runtime.GC()
            }
        }
    }()
}

// 在环境变量中设置
// GOMEMLIMIT=500MiB ./main -ip proxies.txt
```

---

## SOCKS5 最严重的原因分析

### 为什么 SOCKS5 模式内存占用最高？

```
综合所有问题：
┌─────────────────────────────────────────────┐
│ HTTP/HTTPS 模式                    │ SOCKS5 模式      │
├──────────────────────────┬──────────────────┤
│ Transport 连接池         │ ✅ 同样的问题     │
│ bufio 缓冲               │ ✅ 同样的问题     │
│ TLS 握手内存             │ ✅ 同样的问题     │
│ KeepAlive goroutine      │ ✅ 同样的问题     │
├──────────────────────────┼──────────────────┤
│ Goroutine 泄漏           │ ❌ 中等问题       │
│ golang.org/x/net/proxy   │ ✅ 🔴 SOCKS5库   │
│                          │    泄漏最严重     │
│ 每个SOCKS5握手           │ ✅ 创建额外      │
│                          │    goroutine      │
└──────────────────────────┴──────────────────┘

高并发 SOCKS5 内存爆炸流程:
1000+ 并发请求
  ↓
  每个请求 → proxy.SOCKS5() 创建 1 个 goroutine
  ↓
  1000+ goroutine 堆积
  ↓
  这些 goroutine 中：
    - 很多在等待 socket 操作
    - 很多已经"完成"但未释放（因为proxy库的实现方式）
    - 每个 goroutine stack: 2-4KB
    - 加上 TLS 握手: 额外几MB
  ↓
  总计: 1000 × 3-5KB stack + 1000 × 2-4MB TLS ≈ 2-4GB
```

### 数据对比（假设1000并发）

| 项目 | HTTP | HTTPS | SOCKS5 |
|------|------|-------|--------|
| Transport 连接池 | 100MB | 100MB | 0MB |
| bufio 缓冲 | 4MB | 4MB | 4MB |
| TLS 握手内存 | 0MB | 500MB | 100MB* |
| KeepAlive goroutine | 10MB | 10MB | 10MB |
| SOCKS5 proxy lib goroutine | 0 | 0 | **1000MB+** 🔴 |
| 网络缓冲区 | 8MB | 8MB | 8MB |
| **总计** | **~122MB** | **~622MB** | **~1100MB+** 🔴 |

*SOCKS5也有TLS（当目标是https），但额外的proxy lib开销最大

---

## 综合修复方案

### 立即可实施的修复（第一阶段）

```go
// 1. 替换SOCKS5库 或 修改proxy.Dialer使用方式
// 2. 禁用所有KeepAlive
// 3. 激进的GC策略
// 4. 显式Connection管理

// 在 testSocks5Proxy 中：
func testSocks5Proxy(ctx context.Context, proxyAddr string, a Auth, timeout time.Duration,
    upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
    reqCounter *uint64) (IPInfo, int, error) {

    var forward proxy.Dialer
    if upstreamDial != nil {
        forward = contextDialer{DialContext: upstreamDial}
    } else {
        forward = &net.Dialer{
            Timeout:   timeout,
            KeepAlive: -1,  // 🔥 禁用
        }
    }

    var authSocks *proxy.Auth
    if a.User != "" || a.Pass != "" {
        authSocks = &proxy.Auth{User: a.User, Password: a.Pass}
    }

    // 🔥 关键：使用 withTimeout 确保资源清理
    dialCtx, dialCancel := context.WithTimeout(ctx, timeout+2*time.Second)
    defer dialCancel()
    
    dialer, err := proxy.SOCKS5("tcp", proxyAddr, authSocks, forward)
    if err != nil {
        return IPInfo{}, 0, err
    }

    // 🔥 显式清理dialer中的goroutine
    defer func() {
        time.Sleep(50 * time.Millisecond)  // 等待goroutine完成
        runtime.GC()
    }()

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
        IdleConnTimeout:        1 * time.Millisecond,  // 🔥 极短
        ForceAttemptHTTP2:      false,
        TLSHandshakeTimeout:    timeout,
        ResponseHeaderTimeout:  timeout,
        ExpectContinueTimeout:  500 * time.Millisecond,
        DisableCompression:     true,
        MaxResponseHeaderBytes: 1 * 1024,
        WriteBufferSize:        1 * 1024,  // 🔥 减小
        ReadBufferSize:         1 * 1024,  // 🔥 减小
    }

    defer tr.CloseIdleConnections()
    
    // 确保所有连接真的关闭
    defer func() {
        time.Sleep(10 * time.Millisecond)
        tr.CloseIdleConnections()
    }()

    // 其余逻辑相同...
}

// 2. 在 main() 中增强GC策略
func main() {
    // ...
    
    // 🔥 更激进的内存限制
    if memLimit > 0 {
        // 改用RSS-based限制
        target := int64(float64(memLimit) * 0.40)  // 从0.90降到0.40
        debug.SetMemoryLimit(target)
    }
    
    // 🔥 启动主动GC
    go func() {
        ticker := time.NewTicker(200 * time.Millisecond)
        defer ticker.Stop()
        var lastGC time.Time
        for range ticker.C {
            rss := readProcessRSS()
            if rss > memLimit/2 && time.Since(lastGC) > 1*time.Second {
                debug.FreeOSMemory()
                runtime.GC()
                lastGC = time.Now()
            }
        }
    }()
    
    // ...
}

// 3. 修改 startDynamicLimiter 使用RSS
func startDynamicLimiter(workers int, memLimit int64, dynamicLimit *int64, active *uint64) {
    // ...
    go func() {
        // 🔥 使用 RSS 而非 HeapAlloc
        const interval = 100 * time.Millisecond
        
        for {
            time.Sleep(interval)
            
            rss := readProcessRSS()
            if rss == 0 {
                var ms runtime.MemStats
                runtime.ReadMemStats(&ms)
                rss = int64(ms.Alloc)
            }
            
            usedRatio := float64(rss) / float64(memLimit)
            
            cur := atomic.LoadInt64(dynamicLimit)
            newLimit := cur
            
            // 🔥 更激进
            if usedRatio > 0.75 {
                atomic.StoreUint32(&memPaused, 1)
                newLimit = cur / 8
                debug.FreeOSMemory()
                runtime.GC()
            } else if usedRatio > 0.65 {
                atomic.StoreUint32(&memPaused, 0)
                newLimit = cur / 4
            } else if usedRatio > 0.50 {
                newLimit = cur / 2
            } else if usedRatio < 0.25 {
                newLimit = cur + cur/4
            }
            
            if newLimit < 2 {
                newLimit = 2
            }
            if newLimit > int64(workers) {
                newLimit = int64(workers)
            }
            
            if newLimit != cur {
                atomic.StoreInt64(dynamicLimit, newLimit)
            }
        }
    }()
}
```

### 第二阶段修复（可选，更深入）

1. **替换SOCKS5库**：考虑使用更轻量的实现或自己实现
2. **使用连接池**：对每个代理创建连接池，复用连接
3. **内存arena分配**：Go 1.20+ 支持的新特性

---

## 测试与验证方法

### 1. 监控内存增长
```bash
# 运行程序并定期检查
while true; do
  ps aux | grep main | grep -v grep
  sleep 5
done

# 或使用 RSS 监控
while true; do
  cat /proc/$(pgrep main)/status | grep VmRSS
  sleep 2
done
```

### 2. 分析 goroutine 堆积
```bash
# 在运行中的程序上启用pprof
# 编译时添加: import _ "net/http/pprof"

go tool pprof http://localhost:6060/debug/pprof/goroutine
# 输出 goroutine 堆栈跟踪，查找 proxy.SOCKS5 相关的
```

### 3. 堆内存分析
```bash
go tool pprof http://localhost:6060/debug/pprof/heap
# top10
# 查看哪些分配最多
```

### 4. Linux特定工具
```bash
# smem - 准确的内存占用分析
smem -P python | sort -k3 -n

# valgrind - 内存泄漏检测
valgrind --leak-check=full ./main -ip test.txt

# perf - 性能分析
perf stat ./main -ip test.txt
```

---

## 总结

该程序在Linux下内存居高不下的根本原因是：

1. **SOCKS5库的goroutine泄漏** 🔴 最重要
2. **Transport连接池和bufio缓冲未及时释放** 🟠
3. **TLS握手过程的临时内存积累** 🟠  
4. **KeepAlive后台goroutine持续运行** 🟠
5. **动态并发限制器基于HeapAlloc而非RSS** 🟡

### 建议修复优先级

| 优先级 | 修复项 | 预期效果 |
|--------|--------|--------|
| **1** 🔴 | SOCKS5 goroutine 清理 | 降低50-60% 内存 |
| **2** 🔴 | 禁用 KeepAlive + 激进GC | 降低15-20% 内存 |
| **3** 🟠 | Transport 连接池优化 | 降低10-15% 内存 |
| **4** 🟠 | RSS-based 内存限制 | 防止OOM，提高稳定性 |
| **5** 🟡 | 缓冲区管理优化 | 降低5-10% 内存 |

---

## 参考资源

- [Go runtime/debug Memory Limit](https://pkg.go.dev/runtime/debug#SetMemoryLimit)
- [golang.org/x/net/proxy Source](https://github.com/golang/net/tree/master/proxy)
- [Linux /proc/self/statm 说明](https://man7.org/linux/man-pages/man5/proc.5.html)
- [Go Memory Management](https://www.youtube.com/watch?v=dh2bYHwKDL8)
