# Proxy Checker

A high-performance, production-grade proxy validator written in Go that supports HTTP, HTTPS, and SOCKS5 protocols with advanced memory management and resource optimization.

[中文文档](./README_CN.md)

## Features

🚀 **Multi-Protocol Support**
- HTTP proxy detection with automatic fallback
- HTTPS proxy detection with TLS validation
- SOCKS5 proxy detection via golang.org/x/net/proxy
- Automatic protocol detection and order optimization
- Inline authentication support (user:pass@host:port format)
- Multiple authentication credentials from file

🔧 **High Performance & Resource Management**
- Dynamic concurrency control with real-time adjustment
- Automatic resource-based concurrency calculation (CPU/Memory/FD)
- Memory budget system preventing OOM conditions
- Active connection tracking with forced cleanup
- Adaptive GC tuning with GOMEMLIMIT support
- FD (file descriptor) monitoring and throttling
- Emergency pause mechanism for extreme conditions

📊 **Detailed IP Information via API**
- ISP/ASN information (Company.Name or ASN.Name)
- IP type classification (Business/Residential/Hosting/etc.)
- Country/Region detection with location data
- Privacy status detection (VPN/Proxy/Tor/Relay/Hosting)
- Primary API: sni-api.furry.ist/ipapi
- Comprehensive JSON response parsing

💾 **Production-Ready Memory Optimization**
- Minimal RSS footprint: 20-50MB for HTTP, 40-60MB for HTTPS
- SOCKS5 mode with connection pooling and cleanup
- Proper goroutine lifecycle management with context cancellation
- Connection tracking with SO_LINGER optimization
- TCP state management (TIME_WAIT reduction)
- bufio.Reader pooling and TLS handshake optimization

⚙️ **Flexible Configuration**
- Rich command-line interface with sensible defaults
- File or stdin input with streaming processing
- Per-protocol timeout and delay settings
- Multi-level authentication (inline/file/none)
- Upstream proxy chaining support
- CDN IP range auto-detection and skipping
- Verbose mode with error classification
- Real-time progress display with ETA/QPS/IPS metrics

## Installation

### From Release
Download pre-compiled binaries from [releases page](https://github.com/yourusername/proxy-checker/releases):
- `proxy-checker-windows-amd64.exe` - Windows 64-bit
- `proxy-checker-linux-amd64` - Linux 64-bit
- `proxy-checker-linux-arm64` - Linux ARM64 (Raspberry Pi, etc.)

### From Source
```bash
git clone https://github.com/yourusername/proxy-checker.git
cd proxy-checker
go build -o proxy-checker .
```

## Usage

### Basic Usage

```bash
# Check a single proxy
./proxy-checker -proxy "127.0.0.1:8080"

# Check proxies from file
./proxy-checker -file proxies.txt

# Check proxies from stdin
cat proxies.txt | ./proxy-checker

# SOCKS5 proxy
./proxy-checker -proxy "socks5://127.0.0.1:1080"
```

### Input Format

Proxies can be specified in multiple formats (parsed by `parseProxyLine`):

```
# Simple format (auto-detect protocol)
127.0.0.1:8080
example.com:3128

# Explicit protocol with scheme
http://127.0.0.1:8080
https://127.0.0.1:443
socks5://127.0.0.1:1080

# With inline authentication (priority over -auth file)
http://user:pass@127.0.0.1:8080
socks5://username:password@proxy.example.com:1080
user:pass@192.168.1.100:8080

# Pure IP (requires -p or -port flag)
192.168.1.1
2001:db8::1

# IPv6 with port
[2001:db8::1]:8080
```

### Command Line Options

**Input & Output**
```
-ip string
    Proxy list file (required). Formats: IP, host:port, URL, user:pass@host:port
    Lines starting with # or empty lines are ignored

-p, -port string
    Default port when input line has no port (e.g., "443", "8080", "1080")
    Auto-inferred from -mode if not specified

-out string
    Output file for successful proxies only
    Auto-generated if not specified: result_mode-{mode}_port-{port}_{timestamp}.txt
    Format: scheme://host:port#[ISP][IPType][Country]
```

**Test Mode & Protocol**
```
-mode string
    Test mode (default: "auto")
    Options:
      auto   - Test protocols in smart order until first success
      all    - Test all protocols (http/https/socks5) for each proxy
      http   - Test HTTP only
      https  - Test HTTPS only
      socks5 - Test SOCKS5 only (alias: s5)
    Protocol test order optimized by port (443→https first, 1080→socks5 first, etc.)
```

**Performance & Timing**
```
-timeout duration
    Single test timeout per proxy+auth+protocol attempt (default: 10s)
    Includes connection, TLS handshake, HTTP request, and IP info fetch

-delay duration
    Delay after each IP completes (all tests done). Default: 0 (no delay)
    Useful for rate limiting: -delay 10ms

-c int
    Concurrency/workers (default: 0 = auto-calculate)
    Auto-calculation: CPUCount * 2000-3000, capped by memory/FD limits
    Minimum: 1000 workers (can be overridden with -unsafe)

-progress duration
    Progress display interval (default: 1s)
    Shows: IPS, QPS, ETA, success/fail/skip counts, dynamic limit, active workers
```

**Authentication**
```
-auth string
    Optional authentication file (user:pass per line)
    Empty or missing file → no authentication
    Inline auth (user:pass@host) takes priority over -auth file
    Default fallback: searches for "auth.txt" in executable/working directory
```

**Memory & Resource Management**
```
-mem-budget float
    Memory budget ratio for auto-concurrency calculation (default: 0.55)
    Range: 0.0-1.0. Higher = more concurrent workers (if memory allows)

-mem-per-job int
    Estimated bytes per job for concurrency calculation (default: 256KB)
    Lower value = more concurrent workers, but may cause memory pressure

-gc-limit float
    GOMEMLIMIT ratio of physical memory (default: 0.75)
    0 = disable. Higher = less aggressive GC, better performance, higher OOM risk
    Set via debug.SetMemoryLimit()

-unsafe
    Remove all safety limits (memory/FD monitoring, dynamic throttling)
    ⚠️ Risk: OOM or EMFILE errors. Use only for testing or controlled environments
```

**Upstream Proxy (Chaining)**
```
-upstream string
    Upstream proxy address (host:port) for chaining
    Example: -upstream 192.168.1.1:1080

-upstream-mode string
    Upstream proxy protocol (default: "s5")
    Options: s5, socks5, http, https

-upstream-auth string
    Upstream proxy authentication (user:pass)
    Example: -upstream-auth myuser:mypass
```

**CDN & IP Filtering**
```
-skip-cdn
    Auto-skip CDN IP ranges (default: true)
    Fetches CIDR lists from Cloudflare, Fastly, AWS CloudFront
    Skips are counted separately in statistics
```

**Verbose & Debugging**
```
-v
    Verbose mode: output failure details (why=reason, err=error)
    Error classification: timeout, tls, auth, refused, dns, ipinfo, etc.
    Without -v: only success entries written to -out file
```

### Examples

**Basic proxy testing from file:**
```bash
./proxy-checker -ip proxies.txt
# Output: result_mode-auto_port-auto_YYYYMMDD-HHMMSS.txt
```

**Test HTTP proxies with custom port and verbose output:**
```bash
./proxy-checker -ip http_proxies.txt -mode http -p 8080 -v -progress 2s
```

**Test SOCKS5 with authentication file:**
```bash
./proxy-checker -ip socks5_list.txt -mode socks5 -auth credentials.txt -timeout 15s -c 50
```

**High-performance testing with custom concurrency:**
```bash
./proxy-checker -ip large_list.txt -c 5000 -timeout 5s -delay 1ms -out valid.txt
```

**Pipe from curl or wget:**
```bash
curl -s https://example.com/proxies.txt | ./proxy-checker -ip /dev/stdin -mode auto
```

**Upstream proxy chaining (test proxies via another proxy):**
```bash
./proxy-checker -ip test.txt -upstream 192.168.1.1:1080 -upstream-mode s5 -upstream-auth user:pass
```

**Memory-constrained environments:**
```bash
./proxy-checker -ip proxies.txt -mem-budget 0.40 -mem-per-job 512000 -gc-limit 0.60
```

**Disable CDN skipping:**
```bash
./proxy-checker -ip all_ips.txt -skip-cdn=false
```

**Unsafe mode (maximum performance, no protection):**
```bash
./proxy-checker -ip huge_list.txt -c 10000 -unsafe
# ⚠️ Warning: May cause OOM or hit file descriptor limits
```

## Output

**Default text format (written to file specified by -out):**
```
http://203.0.113.42:8080#[Example ISP][isp][US]
https://203.0.113.43:443#[Another ISP][hosting][JP]
socks5://user:pass@203.0.113.44:1080#[Residential ISP][residential][DE]
```
Format: `{scheme}://{auth}@{host:port}#{[ISP][IPType][Country]}`

**Progress output (stderr, real-time):**
```
ips:     1523/10000    left:8477      ip/s:   125.3 qps:   342.1 eta:1m7s       ok:1234   fail:289      skip:0     dyn:2000   act:1876   up:12s
```
- **ips**: completed / total IPs
- **left**: remaining IPs  
- **ip/s**: IPs per second (EMA smoothed)
- **qps**: HTTP requests per second (may be >1 per IP due to retries)
- **eta**: estimated time remaining
- **ok**: successful proxy count
- **fail**: failed proxy count
- **skip**: skipped IPs (CDN, bad format, etc.)
- **dyn**: current dynamic concurrency limit
- **act**: active workers running
- **up**: uptime

**Verbose mode output (stderr with -v flag):**
```
FAIL 203.0.113.100:8080 why=timeout err=context deadline exceeded
FAIL 203.0.113.101:8080 why=auth err=proxy CONNECT failed: HTTP/1.1 407 Proxy Authentication Required
FAIL 203.0.113.102:443 why=tls err=tls: handshake failure
```
Error classification:
- `auth` - 407/401 authentication required
- `timeout` - connection or read timeout
- `tls` - TLS/SSL handshake errors
- `refused` - connection refused
- `unreachable` - network unreachable
- `reset` - connection reset
- `dns` - DNS resolution failure
- `ipinfo` - IP information API error
- `connect_fail` - proxy CONNECT method failed
- `non204` - unexpected preflight response
- `eof` - unexpected EOF
- `other` - unclassified errors

**Final summary (stdout):**
```
done. out=result_mode-auto_port-auto_20260203-143052.txt okIP=1234 okLines=1456 fail=289 skip=15
```

## Performance

**Benchmark results on modern hardware (tested on Linux/Windows):**

| Mode | Workers | IPS (proxies/sec) | QPS (requests/sec) | Memory (RSS) | Notes |
|------|---------|-------------------|-------------------|--------------|-------|
| HTTP | 2000 | 120-180 | 150-220 | 25-40MB | Best memory efficiency |
| HTTPS | 2000 | 80-140 | 100-180 | 50-80MB | TLS overhead |
| SOCKS5 | 1000 | 40-80 | 60-120 | 80-150MB | Higher per-connection cost |
| Auto | 2000 | 100-160 | 140-200 | 30-60MB | Mixed protocols, optimal |
| All | 1500 | 30-60 | 120-240 | 60-120MB | Tests all 3 types per IP |

**Memory scaling (Linux with dynamic limiter enabled):**

| Proxy Count | HTTP Mode | HTTPS Mode | SOCKS5 Mode | Workers |
|-------------|-----------|------------|-------------|---------|
| 100 | 20-30MB | 35-50MB | 60-90MB | 2000 |
| 1000 | 25-40MB | 50-80MB | 100-160MB | 2000 |
| 10000 | 30-60MB | 70-120MB | 150-250MB | 2000-3000 |
| 50000+ | 40-100MB | 100-200MB | 200-400MB | Dynamic (capped) |

**Dynamic concurrency behavior:**
- Starts at calculated max (e.g., 2000-5000 based on CPU/RAM)
- Throttles down by 10-30% when memory >70% or FD >70%
- Emergency pause when memory >88% or FD >85%
- Gradually increases by ~1-2% when resources <60%

## System Requirements

**Minimum:**
- OS: Linux (kernel 3.10+), Windows 10/Server 2016+, macOS 10.14+
- CPU: 1 core (2+ recommended for 1000+ concurrency)
- RAM: 256MB (512MB+ recommended for production)
- Disk: 10MB for binary, variable for output files
- Network: Active internet connection for IP API (sni-api.furry.ist)

**Recommended for Production:**
- CPU: 4+ cores for high-throughput (5000+ workers)
- RAM: 1-2GB for large-scale testing (10k+ proxies)
- File descriptors: 65535+ (`ulimit -n 65535` on Linux)
- Network: Low-latency connection (<50ms to IP API)

**Platform-specific optimizations:**
- Linux: SO_LINGER, TCP_NODELAY, automatic RSS detection
- Windows: Native memory limit detection, Windows-specific syscalls
- Both: FD limit detection, cgroup memory limits (v1/v2), GOMEMLIMIT support

## Supported Platforms

- Windows (x86_64)
- Linux (x86_64, ARM64)
- macOS (x86_64, ARM64)

## Known Issues

⚠️ **SOCKS5 Memory Leak**

The SOCKS5 proxy validation mode has a memory leak issue that causes higher memory consumption during concurrent validation.

**Symptoms:**
- SOCKS5 mode uses 2-5x more memory than HTTP/HTTPS modes
- Memory is not properly released after connections close
- High memory usage with large proxy batches or high concurrency

**Current Status:** ❌ **Not Fixed** - Solution under investigation

**Workarounds:**
1. Reduce concurrent workers: `./proxy-checker -file proxies.txt -scheme socks5 -concurrent 10`
2. Use smaller batch sizes and process in multiple runs
3. Monitor memory with system tools and restart periodically
4. Use HTTP/HTTPS mode for large-scale validation if possible

**Related Documentation:** See `docs/MEMORY_ANALYSIS_CN.md` for technical details of the memory leak analysis.

We appreciate any contributions or insights to help resolve this issue!

---

## Troubleshooting

### "Connection refused" errors
- Verify proxy server is running and accessible
- Check firewall rules
- Confirm proxy address and port are correct

### High memory usage
- Reduce concurrent workers with `-concurrent` flag
- Increase timeout to allow proper cleanup
- Check for IP API rate limiting

### Slow performance
- Increase concurrent workers (if memory permits)
- Use `-timeout` to fail fast on unresponsive proxies
- Check network connectivity and DNS resolution

### "API rate limit exceeded"
- The program includes built-in rate limiting
- Reduce concurrent workers to lower API call frequency
- Wait before retrying the same batch

## Architecture

The checker uses a pipeline architecture:

1. **Parser** - Reads and validates proxy addresses
2. **Worker Pool** - Concurrent proxy validation with memory management
3. **Validator** - Tests proxy connectivity and protocol detection
4. **IP API Client** - Queries detailed IP information
5. **Result Formatter** - Outputs results in various formats

Key memory optimizations:
- Goroutine pool with bounded size
- Connection pooling with proper cleanup
- Batch processing to reduce peak memory
- Garbage collection tuning for long-running processes

## Configuration Files

Optional `config.json` for batch operations:
```json
{
  "timeout": 10,
  "concurrent": 50,
  "batch_size": 100,
  "retry_count": 1,
  "output_file": "results.json"
}
```

## API Integration

The checker uses the following IP information APIs:
- **IP API** - Geographic and ISP information
- **MaxMind GeoIP** - Country/region lookup
- **ABUSE.CH** - Abuse report information

Note: API rate limits apply. The program respects these limits automatically.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MIT License - see the [LICENSE](./LICENSE) file for details.

## Author

Created with ❤️ for the proxy testing community.

## Changelog

See [CHANGELOG.md](./CHANGELOG.md) for version history and updates.

## Support

For issues, questions, or feature requests:
- Open an [issue on GitHub](https://github.com/yourusername/proxy-checker/issues)
- Check [FAQ](./docs/FAQ.md)
- Review [documentation](./docs/)

---

**Note**: This tool is for legitimate network testing and administration purposes only. Always ensure you have permission before testing proxies or network infrastructure.
