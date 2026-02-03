# 代理检测器 (Proxy Checker)

一款高性能、内存优化的代理验证工具，采用Go语言开发，支持HTTP、HTTPS和SOCKS5协议。

[English](./README.md)

## 功能特性

🚀 **多协议支持**
- HTTP代理检测
- HTTPS代理检测
- SOCKS5代理检测
- 自动协议检测

🔧 **高性能并发**
- 智能并发处理，内存使用优化
- 根据可用内存自动调整并发数量
- 支持大规模代理批量验证

📊 **详细IP信息**
- ISP运营商识别
- IP类型分类
- 国家/地区识别
- ASN查询
- 隐私状态检测（VPN/代理/Tor）
- 滥用报告信息

💾 **内存优化**
- Linux下HTTP模式内存占用极低（20-50MB）
- SOCKS5模式专为并发优化
- 完善的goroutine生命周期管理
- 自动垃圾回收调优

⚙️ **灵活配置**
- 命令行界面友好
- 支持文件和stdin输入代理列表
- 可配置超时和重试设置
- 支持用户名/密码认证

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

代理可以使用多种格式指定：

```
# 简单格式（自动检测协议）
127.0.0.1:8080
127.0.0.1:1080

# 指定协议
http://127.0.0.1:8080
https://127.0.0.1:443
socks5://127.0.0.1:1080

# 带认证信息
http://user:pass@127.0.0.1:8080
socks5://user:pass@127.0.0.1:1080
```

### 命令行选项

```
-proxy string
    单个代理地址 (例如: "127.0.0.1:8080")

-file string
    包含代理列表的文件 (每行一个代理)

-scheme string
    协议提示: http, https, socks5 (默认: 自动检测)

-timeout duration
    每个代理连接超时时间 (默认: 10s)

-concurrent int
    并发工作线程数 (默认: 自动)

-membudget float
    自动并发的内存预算比例 (默认: 0.60)

-verbose
    启用详细输出 (默认: 关闭)

-json
    以JSON格式输出结果

-stats
    显示统计信息

-cpuprofile string
    CPU性能分析文件 (用于调试)

-memprofile string
    内存性能分析文件 (用于调试)
```

### 使用示例

**检测文件中的代理并显示详细输出:**
```bash
./proxy-checker -file proxies.txt -verbose -stats
```

**检测SOCKS5代理，自定义超时:**
```bash
./proxy-checker -file socks5_list.txt -scheme socks5 -timeout 15s -concurrent 50
```

**导出为JSON格式:**
```bash
./proxy-checker -file proxies.txt -json > results.json
```

**使用curl管道输入:**
```bash
curl -s https://example.com/proxies.txt | ./proxy-checker -timeout 5s
```

## 输出格式

默认文本输出：
```
IP: 203.0.113.42
  状态: 有效
  类型: HTTP
  响应码: 200
  ISP: 示例ISP
  国家: 美国
  IP类型: 商业
  VPN: 否
  代理: 否
```

JSON格式输出 (使用 `-json` 选项)：
```json
{
  "proxy_addr": "203.0.113.42:8080",
  "proxy_type": "HTTP",
  "success": true,
  "status_code": 200,
  "isp": "示例ISP",
  "country": "US",
  "ip_type": "Commercial",
  "detected_services": {
    "vpn": false,
    "proxy": false,
    "tor": false
  }
}
```

## 性能指标

在典型硬件上的基准测试结果：

| 模式 | 代理数/秒 | 内存占用(100个代理) | 内存占用(1000个代理) |
|------|----------|-------------------|---------------------|
| HTTP | ~100-200 | 20-30MB           | 80-120MB            |
| HTTPS | ~80-150 | 30-50MB           | 100-150MB           |
| SOCKS5 | ~50-100 | 40-60MB           | 150-250MB           |

## 系统要求

- **操作系统**: Linux, Windows, macOS
- **Go**: 1.21+ (用于从源码编译)
- **内存**: 最少256MB (推荐512MB+)
- **网络**: 需要互联网连接用于IP API查询

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
