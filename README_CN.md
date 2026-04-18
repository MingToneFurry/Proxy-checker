# 代理检测器 (Proxy Checker)

一款生产级别、高性能的代理验证工具，采用Go语言开发，支持HTTP、HTTPS和SOCKS5协议，具备先进的内存管理和资源优化功能。

[English](./README.md)

## 功能特性

🚀 **多协议支持**
- HTTP代理检测，支持自动回退
- HTTPS代理检测，带TLS验证
- SOCKS5代理检测（基于 golang.org/x/net/proxy）
- 自动协议检测和智能顺序优化
- 内联认证支持（user:pass@host:port 格式）
- 从文件读取多组认证凭据

🔧 **高性能与资源管理**
- 动态并发控制，实时自适应调整
- 基于资源的自动并发计算（CPU/内存/文件描述符）
- 内存预算系统，防止OOM（内存溢出）
- 主动连接跟踪与强制清理
- 自适应GC调优，支持GOMEMLIMIT
- FD（文件描述符）监控和限流
- 极端情况下的紧急暂停机制

📊 **详细的IP信息查询**
- ISP/ASN信息（Company.Name 或 ASN.Name）
- IP类型分类（商业/住宅/托管等）
- 国家/地区检测，包含地理位置数据
- 隐私状态检测（VPN/代理/Tor/中继/托管）
- 主API接口：sni-api.furry.ist/ipapi
- 完整的JSON响应解析

💾 **生产级内存优化**
- 最小RSS占用：HTTP 20-50MB，HTTPS 40-60MB
- SOCKS5模式带连接池和自动清理
- 完善的goroutine生命周期管理，带上下文取消
- 连接跟踪与SO_LINGER优化
- TCP状态管理（TIME_WAIT减少）
- bufio.Reader池化和TLS握手优化

⚙️ **灵活的配置选项**
- 丰富的命令行界面，默认值合理
- 文件或stdin输入，流式处理
- 按协议设置超时和延迟
- 多级认证（内联/文件/无认证）
- 上游代理链支持
- CDN IP段自动检测和跳过
- 详细模式，带错误分类
- 实时进度显示（ETA/QPS/IPS指标）

## 安装

### 下载预编译版本
从[发布页面](https://github.com/yourusername/proxy-checker/releases)下载：
- `proxy-checker-windows-amd64.exe` - Windows 64位
- `proxy-checker-linux-amd64` - Linux 64位
- `proxy-checker-linux-arm64` - Linux ARM64 (树莓派等)

### 从源码编译
```bash
git clone https://github.com/yourusername/proxy-checker.git
cd proxy-checker
go build -o proxy-checker .
```

## 使用方法

### 基本使用

```bash
# 检测单个代理
./proxy-checker -proxy "127.0.0.1:8080"

# 检测文件中的代理列表
./proxy-checker -file proxies.txt

# 从标准输入读取代理列表
cat proxies.txt | ./proxy-checker

# 检测SOCKS5代理
./proxy-checker -proxy "socks5://127.0.0.1:1080"
```

### 输入格式

代理可以使用多种格式指定（由 `parseProxyLine` 解析）：

```
# 简单格式（自动检测协议）
127.0.0.1:8080
example.com:3128

# 明确指定协议
http://127.0.0.1:8080
https://127.0.0.1:443
socks5://127.0.0.1:1080

# 带内联认证（优先级高于 -auth 文件）
http://user:pass@127.0.0.1:8080
socks5://username:password@proxy.example.com:1080
user:pass@192.168.1.100:8080

# 纯IP（需要 -p 或 -port 参数）
192.168.1.1
2001:db8::1

# IPv6带端口
[2001:db8::1]:8080
```

### 命令行选项

**输入与输出**
```
-ip string
    代理列表文件（必需）。格式：IP、host:port、URL、user:pass@host:port
    以 # 开头或空行会被忽略

-p, -port string
    输入行没有端口时使用的默认端口（例如 "443"、"8080"、"1080"）
    如未指定，会根据 -mode 自动推断

-out string
    仅保存成功代理的输出文件
    如未指定则自动生成：result_mode-{mode}_port-{port}_{timestamp}.txt
    格式：scheme://host:port#[ISP][IPType][Country]
```

**测试模式与协议**
```
-mode string
    测试模式（默认："auto"）
    选项：
      auto   - 按智能顺序测试协议，直到第一个成功
      all    - 为每个代理测试所有协议（http/https/socks5）
      http   - 仅测试HTTP
      https  - 仅测试HTTPS
      socks5 - 仅测试SOCKS5（别名：s5）
    协议测试顺序根据端口优化（443→优先https，1080→优先socks5等）
```

**性能与时间控制**
```
-timeout duration
    单次测试超时时间（每个代理+认证+协议组合）（默认：10s）
    包括连接、TLS握手、HTTP请求和IP信息获取

-delay duration
    每个IP完成后的延迟（所有测试完成后）。默认：0（无延迟）
    用于限速：-delay 10ms

-c int
    并发数/工作线程数（默认：0 = 自动计算）
    自动计算：CPU核心数 * 2000-3000，受内存/FD限制
    最小值：1000个工作线程（可用 -unsafe 覆盖）

-progress duration
    进度显示间隔（默认：1s）
    显示：IPS、QPS、ETA、成功/失败/跳过计数、动态限制、活跃工作线程
```

**认证**
```
-auth string
    可选的认证文件（每行 user:pass）
    空文件或不存在 → 无认证
    内联认证（user:pass@host）优先级高于 -auth 文件
    默认回退：在可执行文件/工作目录中搜索 "auth.txt"
```

**内存与资源管理**
```
-mem-budget float
    自动并发计算的内存预算比例（默认：0.55）
    范围：0.0-1.0。值越高 = 并发工作线程越多（如果内存允许）

-mem-per-job int
    并发计算的单任务预估字节数（默认：256KB）
    值越低 = 并发工作线程越多，但可能造成内存压力

-gc-limit float
    GOMEMLIMIT相对于物理内存的比例（默认：0.75）
    0 = 禁用。值越高 = GC越不激进，性能更好，但OOM风险更高
    通过 debug.SetMemoryLimit() 设置

-unsafe
    移除所有安全限制（内存/FD监控、动态限流）
    ⚠️ 风险：可能导致OOM或EMFILE错误。仅用于测试或受控环境
```

**上游代理（链式代理）**
```
-upstream string
    上游代理地址（host:port）用于链式代理
    示例：-upstream 192.168.1.1:1080

-upstream-mode string
    上游代理协议（默认："s5"）
    选项：s5、socks5、http、https

-upstream-auth string
    上游代理认证（user:pass）
    示例：-upstream-auth myuser:mypass
```

**CDN与IP过滤**
```
-skip-cdn
    自动跳过CDN IP段（默认：true）
    从 Cloudflare、Fastly、AWS CloudFront 获取CIDR列表
    跳过的IP会单独统计
```

**详细与调试**
```
-v
    详细模式：输出失败详情（why=原因，err=错误）
    错误分类：timeout、tls、auth、refused、dns、ipinfo等
    不带 -v：仅将成功条目写入 -out 文件
```

### 使用示例

**从文件检测代理（基础）：**
```bash
./proxy-checker -ip proxies.txt
# 输出：result_mode-auto_port-auto_YYYYMMDD-HHMMSS.txt
```

**测试HTTP代理，自定义端口和详细输出：**
```bash
./proxy-checker -ip http_proxies.txt -mode http -p 8080 -v -progress 2s
```

**测试SOCKS5，使用认证文件：**
```bash
./proxy-checker -ip socks5_list.txt -mode socks5 -auth credentials.txt -timeout 15s -c 50
```

**高性能测试，自定义并发：**
```bash
./proxy-checker -ip large_list.txt -c 5000 -timeout 5s -delay 1ms -out valid.txt
```

**从curl或wget管道输入：**
```bash
curl -s https://example.com/proxies.txt | ./proxy-checker -ip /dev/stdin -mode auto
```

**上游代理链（通过另一个代理测试代理）：**
```bash
./proxy-checker -ip test.txt -upstream 192.168.1.1:1080 -upstream-mode s5 -upstream-auth user:pass
```

**内存受限环境：**
```bash
./proxy-checker -ip proxies.txt -mem-budget 0.40 -mem-per-job 512000 -gc-limit 0.60
```

**禁用CDN跳过：**
```bash
./proxy-checker -ip all_ips.txt -skip-cdn=false
```

**不安全模式（最大性能，无保护）：**
```bash
./proxy-checker -ip huge_list.txt -c 10000 -unsafe
# ⚠️ 警告：可能导致OOM或达到文件描述符限制
```

## 输出格式

**默认文本格式（写入 -out 指定的文件）：**
```
http://203.0.113.42:8080#[Example ISP][isp][US]
https://203.0.113.43:443#[Another ISP][hosting][JP]
socks5://user:pass@203.0.113.44:1080#[Residential ISP][residential][DE]
```
格式：`{scheme}://{auth}@{host:port}#{[ISP][IPType][Country]}`

**进度输出（stderr，实时）：**
```
ips:     1523/10000    left:8477      ip/s:   125.3 qps:   342.1 eta:1m7s       ok:1234   fail:289      skip:0     dyn:2000   act:1876   up:12s
```
- **ips**: 已完成 / 总IP数
- **left**: 剩余IP数  
- **ip/s**: 每秒处理的IP数（EMA平滑）
- **qps**: 每秒HTTP请求数（因重试可能 >1）
- **eta**: 预计剩余时间
- **ok**: 成功的代理数
- **fail**: 失败的代理数
- **skip**: 跳过的IP数（CDN、格式错误等）
- **dyn**: 当前动态并发限制
- **act**: 活跃工作线程数
- **up**: 运行时间

**详细模式输出（stderr，带 -v 参数）：**
```
FAIL 203.0.113.100:8080 why=timeout err=context deadline exceeded
FAIL 203.0.113.101:8080 why=auth err=proxy CONNECT failed: HTTP/1.1 407 Proxy Authentication Required
FAIL 203.0.113.102:443 why=tls err=tls: handshake failure
```
错误分类：
- `auth` - 407/401 需要认证
- `timeout` - 连接或读取超时
- `tls` - TLS/SSL握手错误
- `refused` - 连接被拒绝
- `unreachable` - 网络不可达
- `reset` - 连接重置
- `dns` - DNS解析失败
- `ipinfo` - IP信息API错误
- `connect_fail` - 代理CONNECT方法失败
- `non204` - 预检响应异常
- `eof` - 意外EOF
- `other` - 未分类错误

**最终摘要（stdout）：**
```
done. out=result_mode-auto_port-auto_20260203-143052.txt okIP=1234 okLines=1456 fail=289 skip=15
```

## 性能指标

**现代硬件基准测试结果（在Linux/Windows上测试）：**

| 模式 | 工作线程 | IPS（代理/秒） | QPS（请求/秒） | 内存（RSS） | 备注 |
|------|---------|---------------|---------------|------------|------|
| HTTP | 2000 | 120-180 | 150-220 | 25-40MB | 内存效率最佳 |
| HTTPS | 2000 | 80-140 | 100-180 | 50-80MB | TLS开销 |
| SOCKS5 | 1000 | 40-80 | 60-120 | 80-150MB | 单连接成本较高 |
| Auto | 2000 | 100-160 | 140-200 | 30-60MB | 混合协议，最优 |
| All | 1500 | 30-60 | 120-240 | 60-120MB | 每个IP测试3种类型 |

**内存扩展（Linux启用动态限制器）：**

| 代理数量 | HTTP模式 | HTTPS模式 | SOCKS5模式 | 工作线程 |
|----------|---------|-----------|-----------|---------|
| 100 | 20-30MB | 35-50MB | 60-90MB | 2000 |
| 1000 | 25-40MB | 50-80MB | 100-160MB | 2000 |
| 10000 | 30-60MB | 70-120MB | 150-250MB | 2000-3000 |
| 50000+ | 40-100MB | 100-200MB | 200-400MB | 动态（受限） |

**动态并发行为：**
- 从计算的最大值开始（例如，基于CPU/RAM的2000-5000）
- 当内存 >70% 或 FD >70% 时降低10-30%
- 当内存 >88% 或 FD >85% 时紧急暂停
- 当资源 <60% 时逐步增加约1-2%

## 系统要求

**最低要求：**
- 操作系统：Linux（内核3.10+）、Windows 10/Server 2016+、macOS 10.14+
- CPU：1核心（建议2+核心用于1000+并发）
- 内存：256MB（生产环境建议512MB+）
- 磁盘：二进制文件10MB，输出文件大小可变
- 网络：需要互联网连接访问IP API（sni-api.furry.ist）

**生产环境建议：**
- CPU：4+核心用于高吞吐量（5000+工作线程）
- 内存：1-2GB用于大规模测试（10k+代理）
- 文件描述符：65535+（Linux上 `ulimit -n 65535`）
- 网络：低延迟连接（到IP API <50ms）

**平台特定优化：**
- Linux：SO_LINGER、TCP_NODELAY、自动RSS检测
- Windows：原生内存限制检测、Windows特定系统调用
- 两者：FD限制检测、cgroup内存限制（v1/v2）、GOMEMLIMIT支持

## 支持的平台

- Windows (x86_64)
- Linux (x86_64, ARM64)
- macOS (x86_64, ARM64)

## 已知问题

⚠️ **SOCKS5 内存泄漏**

SOCKS5代理验证模式存在内存泄漏问题，导致并发验证时内存占用过高。

**表现症状：**
- SOCKS5模式内存占用是HTTP/HTTPS模式的2-5倍
- 连接关闭后内存无法正确释放
- 大规模代理批次或高并发下内存占用严重

**当前状态：** ❌ **未解决** - 正在调查解决方案

**临时方案：**
1. 减少并发数量：`./proxy-checker -file proxies.txt -scheme socks5 -concurrent 10`
2. 分小批处理，多次运行处理完整列表
3. 使用系统工具监控内存，定期重启程序
4. 如可能，优先使用HTTP/HTTPS模式进行大规模验证

**相关资料：** 查看 `docs/MEMORY_ANALYSIS_CN.md` 了解内存泄漏的技术分析细节。

欢迎任何贡献或建议来帮助解决这个问题！

---

## 故障排查

### "连接被拒绝"错误
- 验证代理服务器是否运行且可访问
- 检查防火墙规则
- 确认代理地址和端口正确

### 内存占用过高
- 使用 `-concurrent` 选项减少并发数量
- 增加超时时间以允许正确的清理
- 检查IP API的速率限制

### 性能缓慢
- 增加并发数量（如果内存允许）
- 使用 `-timeout` 快速过滤无响应的代理
- 检查网络连接和DNS解析

### "API速率限制超出"
- 程序包含内置速率限制机制
- 减少并发数量以降低API调用频率
- 等待后重试相同的代理批次

## 架构设计

检测器使用管道架构：

1. **解析器** - 读取和验证代理地址
2. **工作池** - 带内存管理的并发代理验证
3. **验证器** - 测试代理连接性和协议检测
4. **IP API客户端** - 查询详细IP信息
5. **结果格式化器** - 以多种格式输出结果

主要内存优化：
- 有界限的goroutine池
- 正确清理的连接池
- 批处理以减少峰值内存
- 长运行进程的垃圾回收调优

## 配置文件

可选的 `config.json` 用于批量操作：
```json
{
  "timeout": 10,
  "concurrent": 50,
  "batch_size": 100,
  "retry_count": 1,
  "output_file": "results.json"
}
```

## API集成

检测器使用以下IP信息APIs：
- **IP API** - 地理位置和ISP信息
- **MaxMind GeoIP** - 国家/地区查询
- **ABUSE.CH** - 滥用报告信息

注意：API有速率限制。程序会自动遵守这些限制。

## 贡献指南

欢迎提交Pull Request！

## 许可证

本项目采用MIT许可证 - 详见 [LICENSE](./LICENSE) 文件。

## 作者

用❤️为代理测试社区而创建。

## 更新日志

查看 [CHANGELOG.md](./CHANGELOG.md) 了解版本历史和更新内容。

## 技术支持

如有问题、疑问或功能请求：
- 在[GitHub上提交issue](https://github.com/yourusername/proxy-checker/issues)
- 查看[常见问题](./docs/FAQ.md)
- 查阅[文档](./docs/)

---

**声明**: 本工具仅用于合法网络测试和管理目的。使用前请确保获得相应权限。
