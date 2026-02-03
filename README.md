# Proxy Checker

A high-performance, memory-efficient proxy validator written in Go that supports HTTP, HTTPS, and SOCKS5 protocols.

[中文文档](./README_CN.md)

## Features

🚀 **Multi-Protocol Support**
- HTTP proxy detection
- HTTPS proxy detection  
- SOCKS5 proxy detection
- Automatic protocol detection

🔧 **High Performance**
- Concurrent batch processing with optimized memory usage
- Automatic concurrency adjustment based on available memory
- Support for large-scale proxy validation

📊 **Detailed IP Information**
- ISP information
- IP type classification
- Country/Region detection
- ASN lookup
- Privacy status (VPN/Proxy/Tor detection)
- Abuse report information

💾 **Memory Optimized**
- Minimal memory footprint on Linux (20-50MB for HTTP mode)
- SOCKS5 mode optimized for concurrent usage
- Proper goroutine lifecycle management
- Automatic garbage collection tuning

⚙️ **Flexible Configuration**
- Command-line interface
- Support for proxy lists from files or stdin
- Configurable timeout and retry settings
- Username/password authentication support

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

Proxies can be specified in multiple formats:

```
# Simple format (auto-detect protocol)
127.0.0.1:8080
127.0.0.1:1080

# Explicit protocol
http://127.0.0.1:8080
https://127.0.0.1:443
socks5://127.0.0.1:1080

# With authentication
http://user:pass@127.0.0.1:8080
socks5://user:pass@127.0.0.1:1080
```

### Command Line Options

```
-proxy string
    Single proxy address to test (e.g., "127.0.0.1:8080")

-file string
    File containing list of proxies (one per line)

-scheme string
    Scheme hint: http, https, socks5 (default: auto-detect)

-timeout duration
    Timeout for each proxy connection (default: 10s)

-concurrent int
    Number of concurrent workers (default: auto)

-membudget float
    Memory budget ratio for auto concurrency (default: 0.60)

-verbose
    Enable verbose output (default: false)

-json
    Output results in JSON format

-stats
    Show statistics at the end

-cpuprofile string
    CPU profiling file (for debugging)

-memprofile string
    Memory profiling file (for debugging)
```

### Examples

**Check proxies from file with verbose output:**
```bash
./proxy-checker -file proxies.txt -verbose -stats
```

**Check SOCKS5 proxies with custom timeout:**
```bash
./proxy-checker -file socks5_list.txt -scheme socks5 -timeout 15s -concurrent 50
```

**Export results to JSON:**
```bash
./proxy-checker -file proxies.txt -json > results.json
```

**Pipe from curl:**
```bash
curl -s https://example.com/proxies.txt | ./proxy-checker -timeout 5s
```

## Output

Default text output:
```
IP: 203.0.113.42
  Status: VALID
  Type: HTTP
  Response Code: 200
  ISP: Example ISP
  Country: US
  IP Type: Commercial
  VPN: No
  Proxy: No
```

JSON output (with `-json` flag):
```json
{
  "proxy_addr": "203.0.113.42:8080",
  "proxy_type": "HTTP",
  "success": true,
  "status_code": 200,
  "isp": "Example ISP",
  "country": "US",
  "ip_type": "Commercial",
  "detected_services": {
    "vpn": false,
    "proxy": false,
    "tor": false
  }
}
```

## Performance

Benchmark results on typical hardware:

| Mode | Proxies/sec | Memory (100 proxies) | Memory (1000 proxies) |
|------|-------------|---------------------|----------------------|
| HTTP | ~100-200    | 20-30MB             | 80-120MB             |
| HTTPS | ~80-150     | 30-50MB             | 100-150MB            |
| SOCKS5 | ~50-100    | 40-60MB             | 150-250MB            |

## System Requirements

- **OS**: Linux, Windows, macOS
- **Go**: 1.21+ (for building from source)
- **Memory**: Minimum 256MB (recommended 512MB+)
- **Network**: Active internet connection for IP API calls

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
