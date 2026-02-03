package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/proxy"
)

type Auth struct {
	User string
	Pass string
}

type Job struct {
	ProxyAddr  string // host:port
	SchemeHint string // "", "http", "https", "socks5"
	InlineAuth *Auth  // 若输入行里带了 user:pass@，则优先用这个
	RawLine    string // 仅用于 debug/统计
}

type Result struct {
	ProxyAddr  string
	Auth       Auth
	ProxyType  string // "SOCKS5" / "HTTP" / "HTTPS"
	Success    bool
	StatusCode int
	Err        error

	ISP     string
	IPType  string
	Country string
}

const (
	defaultTimeout = 10 * time.Second

	primaryIPAPI = "https://sni-api.furry.ist/ipapi"

	// 模拟 Chrome 144 浏览器
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36"
)

var (
	memBudgetRatio float64 = 0.60       // 自动并发时用于内存预算的比例
	memPerJobBytes int64   = 128 * 1024 // 自动并发时假定每个任务占用的内存
	gcLimitRatio   float64 = 0.80       // debug.SetMemoryLimit 占物理内存比例
)

// ========== IP API 响应结构 ==========
type IPAPIASNResp struct {
	ASN    string `json:"asn"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
	Route  string `json:"route"`
	Type   string `json:"type"`
}

type IPAPICompanyResp struct {
	Name   string `json:"name"`
	Domain string `json:"domain"`
	Type   string `json:"type"`
}

type IPAPIPrivacyResp struct {
	VPN     bool   `json:"vpn"`
	Proxy   bool   `json:"proxy"`
	Tor     bool   `json:"tor"`
	Relay   bool   `json:"relay"`
	Hosting bool   `json:"hosting"`
	Service string `json:"service"`
}

type IPAPIAbuseResp struct {
	Address string `json:"address"`
	Country string `json:"country"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Network string `json:"network"`
	Phone   string `json:"phone"`
}

type IPAPIDomainsResp struct {
	Page    int      `json:"page"`
	Total   int      `json:"total"`
	Domains []string `json:"domains"`
}

type IPAPIDataResp struct {
	IP       string           `json:"ip"`
	Hostname string           `json:"hostname"`
	City     string           `json:"city"`
	Region   string           `json:"region"`
	Country  string           `json:"country"`
	Loc      string           `json:"loc"`
	Postal   string           `json:"postal"`
	Timezone string           `json:"timezone"`
	ASN      IPAPIASNResp     `json:"asn"`
	Company  IPAPICompanyResp `json:"company"`
	Privacy  IPAPIPrivacyResp `json:"privacy"`
	Abuse    IPAPIAbuseResp   `json:"abuse"`
	Domains  IPAPIDomainsResp `json:"domains"`
}

type IPAPIResp struct {
	IPAPI IPAPIDataResp `json:"ipapi"`
	Code  int           `json:"code"`
}

type IPInfo struct {
	ISP        string
	IPType     string
	Country    string
	StatusCode int
	Source     string
}

// ========== HTTP/HTTPS 代理拨号器 ==========
type HTTPProxyDialer struct {
	addr     string
	auth     *Auth
	useTLS   bool
	timeout  time.Duration
	baseDial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func newDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout:   timeout,
		KeepAlive: -1,
		Control: func(network, address string, c syscall.RawConn) error {
			var ctlErr error
			_ = c.Control(func(fd uintptr) {
				ctlErr = setSockLinger(fd)
			})
			return ctlErr
		},
	}
}

func (d *HTTPProxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var conn net.Conn
	var err error

	if d.baseDial != nil {
		conn, err = d.baseDial(ctx, "tcp", d.addr)
	} else {
		nd := newDialer(d.timeout)
		if dl, ok := ctx.Deadline(); ok {
			nd.Deadline = dl
		}
		conn, err = nd.DialContext(ctx, "tcp", d.addr)
	}
	if err != nil {
		return nil, err
	}

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	if d.useTLS {
		host, _, splitErr := net.SplitHostPort(d.addr)
		if splitErr != nil || host == "" {
			host = d.addr
		}
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true,
		})
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			return nil, err
		}
		conn = tlsConn
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: %s\r\n", addr, addr, userAgent)
	if d.auth != nil && d.auth.User != "" {
		cred := d.auth.User + ":" + d.auth.Pass
		b64 := base64.StdEncoding.EncodeToString([]byte(cred))
		fmt.Fprintf(&sb, "Proxy-Authorization: Basic %s\r\n", b64)
	}
	sb.WriteString("\r\n")

	if _, err := io.WriteString(conn, sb.String()); err != nil {
		_ = conn.Close()
		return nil, err
	}

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	sl := strings.TrimSpace(statusLine)
	if !(strings.HasPrefix(sl, "HTTP/1.1 200") || strings.HasPrefix(sl, "HTTP/1.0 200") || strings.Contains(sl, " 200 ")) {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy CONNECT failed: %s", sl)
	}

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

type contextDialer struct {
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (d contextDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

// ========== 计数 RoundTripper（用于 QPS） ==========
type countingRoundTripper struct {
	base    http.RoundTripper
	counter *uint64
}

func (c countingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if c.counter != nil {
		atomic.AddUint64(c.counter, 1)
	}
	return c.base.RoundTrip(req)
}

// ========== 连接跟踪器 - 强制关闭所有连接 ==========
type trackedConn struct {
	net.Conn
	tracker *connTracker
}

func (c *trackedConn) Close() error {
	c.tracker.remove(c.Conn)
	return c.Conn.Close()
}

type connTracker struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func newConnTracker() *connTracker {
	return &connTracker{conns: make(map[net.Conn]struct{})}
}

func (t *connTracker) track(conn net.Conn) net.Conn {
	t.mu.Lock()
	t.conns[conn] = struct{}{}
	t.mu.Unlock()
	return &trackedConn{Conn: conn, tracker: t}
}

func (t *connTracker) remove(conn net.Conn) {
	t.mu.Lock()
	delete(t.conns, conn)
	t.mu.Unlock()
}

func (t *connTracker) closeAll() {
	t.mu.Lock()
	for conn := range t.conns {
		_ = conn.Close()
	}
	t.conns = make(map[net.Conn]struct{})
	t.mu.Unlock()
}

// ========== CDN 跳过（联网获取 CIDR） ==========
type cidrEntry struct {
	Provider string
	Net      *net.IPNet
}

type CDNFilter struct {
	V4 []cidrEntry
	V6 []cidrEntry
}

func (f *CDNFilter) Match(ip net.IP) (string, bool) {
	if ip == nil {
		return "", false
	}
	if ip4 := ip.To4(); ip4 != nil {
		for _, e := range f.V4 {
			if e.Net.Contains(ip4) {
				return e.Provider, true
			}
		}
		return "", false
	}
	for _, e := range f.V6 {
		if e.Net.Contains(ip) {
			return e.Provider, true
		}
	}
	return "", false
}

func (f *CDNFilter) addCIDR(provider, cidr string) {
	_, n, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil || n == nil {
		return
	}
	if strings.Contains(cidr, ":") {
		f.V6 = append(f.V6, cidrEntry{Provider: provider, Net: n})
	} else {
		f.V4 = append(f.V4, cidrEntry{Provider: provider, Net: n})
	}
}

func fetchTextFields(ctx context.Context, client *http.Client, u string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("status=%d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(b)), nil
}

func loadCDNFilter(ctx context.Context) (*CDNFilter, error) {
	client := &http.Client{Timeout: 12 * time.Second}
	f := &CDNFilter{}

	// Cloudflare
	{
		fields, err := fetchTextFields(ctx, client, "https://www.cloudflare.com/ips-v4")
		if err == nil {
			for _, s := range fields {
				f.addCIDR("cloudflare", s)
			}
		}
		fields6, err6 := fetchTextFields(ctx, client, "https://www.cloudflare.com/ips-v6")
		if err6 == nil {
			for _, s := range fields6 {
				f.addCIDR("cloudflare", s)
			}
		}
	}

	// Fastly
	{
		type fastlyResp struct {
			Addresses     []string `json:"addresses"`
			IPv6Addresses []string `json:"ipv6_addresses"`
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.fastly.com/public-ip-list", nil)
		if err == nil {
			req.Header.Set("User-Agent", userAgent)
			resp, err := client.Do(req)
			if err == nil {
				func() {
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						_, _ = io.Copy(io.Discard, resp.Body)
						return
					}
					var fr fastlyResp
					dec := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
					if err := dec.Decode(&fr); err != nil {
						return
					}
					for _, s := range fr.Addresses {
						f.addCIDR("fastly", s)
					}
					for _, s := range fr.IPv6Addresses {
						f.addCIDR("fastly", s)
					}
				}()
			}
		}
	}

	// AWS CloudFront
	{
		type awsRanges struct {
			Prefixes []struct {
				IPPrefix string `json:"ip_prefix"`
				Region   string `json:"region"`
				Service  string `json:"service"`
			} `json:"prefixes"`
			IPv6Prefixes []struct {
				IPv6Prefix string `json:"ipv6_prefix"`
				Region     string `json:"region"`
				Service    string `json:"service"`
			} `json:"ipv6_prefixes"`
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ip-ranges.amazonaws.com/ip-ranges.json", nil)
		if err == nil {
			req.Header.Set("User-Agent", userAgent)
			resp, err := client.Do(req)
			if err == nil {
				func() {
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						_, _ = io.Copy(io.Discard, resp.Body)
						return
					}
					var ar awsRanges
					dec := json.NewDecoder(io.LimitReader(resp.Body, 32<<20))
					if err := dec.Decode(&ar); err != nil {
						return
					}
					for _, p := range ar.Prefixes {
						if p.Service == "CLOUDFRONT" && p.Region == "GLOBAL" && p.IPPrefix != "" {
							f.addCIDR("cloudfront", p.IPPrefix)
						}
					}
					for _, p := range ar.IPv6Prefixes {
						if p.Service == "CLOUDFRONT" && p.Region == "GLOBAL" && p.IPv6Prefix != "" {
							f.addCIDR("cloudfront", p.IPv6Prefix)
						}
					}
				}()
			}
		}
	}

	if len(f.V4) == 0 && len(f.V6) == 0 {
		return nil, fmt.Errorf("cdn cidr empty (all sources failed?)")
	}
	return f, nil
}

// ========== 统计计数器（失败原因/跳过原因） ==========
type CounterMap struct {
	m sync.Map // key string -> *uint64
}

func (c *CounterMap) Inc(key string) {
	if key == "" {
		return
	}
	v, _ := c.m.LoadOrStore(key, new(uint64))
	atomic.AddUint64(v.(*uint64), 1)
}

func (c *CounterMap) Snapshot() map[string]uint64 {
	out := make(map[string]uint64)
	c.m.Range(func(k, v any) bool {
		out[k.(string)] = atomic.LoadUint64(v.(*uint64))
		return true
	})
	return out
}

func formatTopStats(cm *CounterMap, topN int) string {
	if cm == nil || topN <= 0 {
		return "-"
	}
	m := cm.Snapshot()
	if len(m) == 0 {
		return "-"
	}
	type kv struct {
		k string
		v uint64
	}
	arr := make([]kv, 0, len(m))
	for k, v := range m {
		if v == 0 {
			continue
		}
		arr = append(arr, kv{k: k, v: v})
	}
	if len(arr) == 0 {
		return "-"
	}
	sort.Slice(arr, func(i, j int) bool {
		if arr[i].v == arr[j].v {
			return arr[i].k < arr[j].k
		}
		return arr[i].v > arr[j].v
	})
	if len(arr) > topN {
		arr = arr[:topN]
	}
	var b strings.Builder
	for i, it := range arr {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%s:%d", it.k, it.v)
	}
	return b.String()
}

// ========== 输入解析 ==========
func parseProxyLine(line, defaultPort string) (addr string, schemeHint string, inlineAuth *Auth, err error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", nil, fmt.Errorf("empty line")
	}

	defaultPortForScheme := func(scheme string) string {
		switch strings.ToLower(strings.TrimSpace(scheme)) {
		case "http":
			return "80"
		case "https":
			return "443"
		case "socks5", "s5":
			return "1080"
		default:
			return ""
		}
	}

	if strings.Contains(line, "://") {
		u, e := url.Parse(line)
		if e == nil && u != nil && u.Host != "" {
			host := u.Hostname()
			port := u.Port()
			if port == "" {
				if defaultPort == "" {
					defaultPort = defaultPortForScheme(u.Scheme)
				}
				if defaultPort == "" {
					return "", "", nil, fmt.Errorf("missing port in %q; use -p", line)
				}
				port = defaultPort
			}
			hostport := net.JoinHostPort(host, port)
			schemeHint = strings.ToLower(strings.TrimSpace(u.Scheme))
			if u.User != nil {
				user := u.User.Username()
				pass, _ := u.User.Password()
				if user != "" || pass != "" {
					inlineAuth = &Auth{User: user, Pass: pass}
				}
			}
			return hostport, schemeHint, inlineAuth, nil
		}
	}

	if strings.Contains(line, "@") && !strings.Contains(line, "://") {
		u, e := url.Parse("http://" + line)
		if e == nil && u != nil && u.Host != "" {
			host := u.Hostname()
			port := u.Port()
			if port == "" {
				if defaultPort == "" {
					return "", "", nil, fmt.Errorf("missing port in %q; use -p", line)
				}
				port = defaultPort
			}
			hostport := net.JoinHostPort(host, port)
			if u.User != nil {
				user := u.User.Username()
				pass, _ := u.User.Password()
				if user != "" || pass != "" {
					inlineAuth = &Auth{User: user, Pass: pass}
				}
			}
			return hostport, "", inlineAuth, nil
		}
	}

	if ip := net.ParseIP(line); ip != nil {
		if defaultPort == "" {
			return "", "", nil, fmt.Errorf("pure ip %s missing port; use -p", line)
		}
		if strings.Contains(line, ":") {
			return "[" + line + "]:" + defaultPort, "", nil, nil
		}
		return line + ":" + defaultPort, "", nil, nil
	}

	if strings.Contains(line, ":") {
		if host, port, e := net.SplitHostPort(line); e == nil {
			return net.JoinHostPort(host, port), "", nil, nil
		}
		if ip := net.ParseIP(strings.Trim(line, "[]")); ip != nil {
			if defaultPort == "" {
				return "", "", nil, fmt.Errorf("missing port in %q; use -p", line)
			}
			return net.JoinHostPort(ip.String(), defaultPort), "", nil, nil
		}
		if defaultPort != "" {
			return net.JoinHostPort(line, defaultPort), "", nil, nil
		}
		return "", "", nil, fmt.Errorf("missing port in %q; use -p", line)
	}

	if defaultPort == "" {
		return "", "", nil, fmt.Errorf("host %s missing port; use -p", line)
	}
	return net.JoinHostPort(line, defaultPort), "", nil, nil
}

func guessProxyOrder(addr string) []string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return []string{"https", "http", "socks5"}
	}
	switch port {
	case "443", "8443", "9443":
		return []string{"https", "http", "socks5"}
	case "80", "8080", "3128", "8000", "8888":
		return []string{"http", "https", "socks5"}
	case "1080":
		return []string{"socks5", "http", "https"}
	default:
		return []string{"https", "http", "socks5"}
	}
}

func guessProxyOrderWithScheme(addr, scheme string) []string {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	switch scheme {
	case "http":
		return []string{"http", "https", "socks5"}
	case "https":
		return []string{"https", "http", "socks5"}
	case "socks5", "s5":
		return []string{"socks5", "http", "https"}
	default:
		return guessProxyOrder(addr)
	}
}

func hostFromHostPort(hp string) string {
	host, _, err := net.SplitHostPort(hp)
	if err != nil {
		return hp
	}
	return host
}

func parseStatusFromText(s string) (int, bool) {
	for i := 0; i+3 <= len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' && s[i+1] >= '0' && s[i+1] <= '9' && s[i+2] >= '0' && s[i+2] <= '9' {
			code, err := strconv.Atoi(s[i : i+3])
			if err == nil {
				return code, true
			}
		}
	}
	return 0, false
}

// ========== Auth 读取（可选） ==========
func loadAuthsOptional(path string) ([]Auth, error) {
	// 如果路径为空，尝试使用同目录的 auth.txt 作为默认文件
	if strings.TrimSpace(path) == "" {
		// 检查当前目录或可执行文件目录下是否有 auth.txt
		ex, err := os.Executable()
		if err == nil {
			exDir := filepath.Dir(ex)
			defaultAuthPath := filepath.Join(exDir, "auth.txt")
			if _, err := os.Stat(defaultAuthPath); err == nil {
				path = defaultAuthPath
			} else {
				// 也检查工作目录
				if _, err := os.Stat("auth.txt"); err == nil {
					path = "auth.txt"
				}
			}
		}

		// 如果仍然没有找到认证文件，返回一个空认证（不带用户名密码）
		if strings.TrimSpace(path) == "" {
			return []Auth{{}}, nil
		}
	}

	f, err := os.Open(path)
	if err != nil {
		// 如果是默认路径，返回空认证而不是报错
		if path == "auth.txt" || strings.HasSuffix(path, "/auth.txt") || strings.HasSuffix(path, "\\auth.txt") {
			return []Auth{{}}, nil
		}
		return nil, err
	}
	defer f.Close()

	var res []Auth
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		res = append(res, Auth{User: strings.TrimSpace(parts[0]), Pass: strings.TrimSpace(parts[1])})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(res) == 0 {
		return []Auth{{}}, nil
	}
	return res, nil
}

// ========== 上游 dialer（可选） ==========
func buildUpstreamDialer(mode, addr string, auth Auth, timeout time.Duration) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	if addr == "" {
		return nil, nil
	}
	mode = strings.ToLower(mode)
	switch mode {
	case "s5", "socks5":
		var sAuth *proxy.Auth
		if auth.User != "" || auth.Pass != "" {
			sAuth = &proxy.Auth{User: auth.User, Password: auth.Pass}
		}
		base := newDialer(timeout)
		d, err := proxy.SOCKS5("tcp", addr, sAuth, base)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, network, target string) (net.Conn, error) { return d.Dial(network, target) }, nil
	case "http", "https":
		var a *Auth
		if auth.User != "" || auth.Pass != "" {
			a = &auth
		}
		httpDialer := &HTTPProxyDialer{addr: addr, auth: a, useTLS: mode == "https", timeout: timeout}
		return httpDialer.DialContext, nil
	default:
		return nil, fmt.Errorf("unsupported upstream mode: %s", mode)
	}
}

// ========== 拉 IP 信息 ==========
func fetchIPInfoWithClient(ctx context.Context, client *http.Client) (IPInfo, error) {
	var info IPInfo

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, primaryIPAPI, nil)
	if err != nil {
		return info, err
	}

	// 模拟 Chrome 浏览器的请求头
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return info, fmt.Errorf("ipinfo request failed: %v", err)
	}
	defer resp.Body.Close()

	info.StatusCode = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return info, fmt.Errorf("ipinfo status=%d", resp.StatusCode)
	}

	// 限制读取大小为32KB
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return info, fmt.Errorf("ipinfo read body failed: %v", err)
	}

	// 检查响应是否像有效的JSON（简单检查开头是否为 { ）
	bodyStr := strings.TrimSpace(string(body))
	if len(bodyStr) == 0 || bodyStr[0] != '{' {
		return info, fmt.Errorf("ipinfo invalid response: not JSON")
	}

	var data IPAPIResp
	if err := json.Unmarshal(body, &data); err != nil {
		return info, fmt.Errorf("ipinfo json parse failed: %v", err)
	}

	// 检查 API 返回码
	if data.Code != 200 {
		return info, fmt.Errorf("ipinfo api error: code=%d", data.Code)
	}

	// 提取有用信息
	ipData := data.IPAPI

	// ISP: 优先使用 Company.Name, 其次使用 ASN.Name
	if ipData.Company.Name != "" {
		info.ISP = strings.TrimSpace(ipData.Company.Name)
	} else if ipData.ASN.Name != "" {
		info.ISP = strings.TrimSpace(ipData.ASN.Name)
	}

	// IPType: 使用 ASN.Type 或 Company.Type
	if ipData.ASN.Type != "" {
		info.IPType = strings.TrimSpace(ipData.ASN.Type)
	} else if ipData.Company.Type != "" {
		info.IPType = strings.TrimSpace(ipData.Company.Type)
	}

	// Country: 使用国家代码
	info.Country = strings.TrimSpace(ipData.Country)
	info.Source = "sni-api.furry.ist"

	// 验证是否获取到有效数据：至少有一个字段非空且 Country 非空才算成功
	if ipData.Country == "" {
		return info, fmt.Errorf("ipinfo invalid response: missing country")
	}

	return info, nil
}

// ========== 错误分类 ==========
func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return false
}

func isDialLikeErr(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Op == "dial" {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") {
		return true
	}
	return false
}

func isAuthStatus(code int) bool {
	return code == http.StatusProxyAuthRequired || code == http.StatusUnauthorized
}

func classifyErr(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	if isHTTPSClientGotHTTP(err) {
		return "https_to_http"
	}

	if strings.Contains(msg, "proxy connect failed") || strings.Contains(msg, "connect failed") {
		if code, ok := parseStatusFromText(msg); ok && isAuthStatus(code) {
			return "auth"
		}
		return "connect_fail"
	}

	if strings.Contains(msg, "preflight non-204") {
		if code, ok := parseStatusFromText(msg); ok && isAuthStatus(code) {
			return "auth"
		}
		return "non204"
	}

	if strings.Contains(msg, "proxy authentication") || strings.Contains(msg, "407") {
		return "auth"
	}
	if isTimeoutErr(err) {
		return "timeout"
	}
	if strings.Contains(msg, "tls") || strings.Contains(msg, "handshake") {
		return "tls"
	}
	if strings.Contains(msg, "no such host") {
		return "dns"
	}
	if isDialLikeErr(err) {
		if strings.Contains(msg, "connection refused") {
			return "refused"
		}
		if strings.Contains(msg, "no route") || strings.Contains(msg, "unreachable") {
			return "unreachable"
		}
		if strings.Contains(msg, "reset") {
			return "reset"
		}
		return "dial"
	}
	if strings.Contains(msg, "ipinfo") || strings.Contains(msg, "unmarshal") || strings.Contains(msg, "json") {
		return "ipinfo"
	}
	if strings.Contains(msg, "eof") {
		return "eof"
	}
	return "other"
}

func isHTTPSClientGotHTTP(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "server gave http response to https client")
}

func isLikelyPlainHTTPProxy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return isHTTPSClientGotHTTP(err) ||
		strings.Contains(msg, "first record does not look like a tls handshake") ||
		strings.Contains(msg, "tls: handshake failure") ||
		strings.Contains(msg, "tls: internal error")
}

// ========== 具体测试 ==========
func testHTTPProxy(ctx context.Context, proxyAddr string, a Auth, timeout time.Duration,
	upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
	reqCounter *uint64) (IPInfo, int, error) {

	proxyURL := &url.URL{Scheme: "http", Host: proxyAddr}
	if a.User != "" || a.Pass != "" {
		proxyURL.User = url.UserPassword(a.User, a.Pass)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 连接跟踪器
	tracker := newConnTracker()
	defer tracker.closeAll()

	baseDialer := newDialer(timeout / 2)

	tr := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var conn net.Conn
			var err error
			if upstreamDial != nil {
				conn, err = upstreamDial(ctx, network, addr)
			} else {
				conn, err = baseDialer.DialContext(ctx, network, addr)
			}
			if err != nil {
				return nil, err
			}
			return tracker.track(conn), nil
		},
		TLSClientConfig:        &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives:      true,
		MaxIdleConns:           1,
		MaxIdleConnsPerHost:    1,
		MaxConnsPerHost:        1,
		IdleConnTimeout:        300 * time.Millisecond,
		ForceAttemptHTTP2:      false,
		TLSHandshakeTimeout:    timeout / 2,
		ResponseHeaderTimeout:  timeout / 2,
		ExpectContinueTimeout:  100 * time.Millisecond,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 4 * 1024,
		WriteBufferSize:        4 * 1024,
		ReadBufferSize:         4 * 1024,
	}

	if a.User != "" || a.Pass != "" {
		h := make(http.Header)
		cred := base64.StdEncoding.EncodeToString([]byte(a.User + ":" + a.Pass))
		h.Set("Proxy-Authorization", "Basic "+cred)
		tr.ProxyConnectHeader = h
	}

	rt := countingRoundTripper{base: tr, counter: reqCounter}
	client := &http.Client{Transport: rt, Timeout: timeout}

	info, err := fetchIPInfoWithClient(ctx, client)
	tr.CloseIdleConnections()
	return info, info.StatusCode, err
}

func testHTTPSProxy(ctx context.Context, proxyAddr string, a Auth, timeout time.Duration,
	upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
	reqCounter *uint64) (IPInfo, int, error) {

	var cred *Auth
	if a.User != "" || a.Pass != "" {
		cred = &a
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 连接跟踪器
	tracker := newConnTracker()
	defer tracker.closeAll()

	hpd := &HTTPProxyDialer{
		addr:    proxyAddr,
		auth:    cred,
		useTLS:  true,
		timeout: timeout / 2,
		baseDial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var conn net.Conn
			var err error
			if upstreamDial != nil {
				conn, err = upstreamDial(ctx, network, addr)
			} else {
				d := newDialer(timeout / 2)
				if dl, ok := ctx.Deadline(); ok {
					d.Deadline = dl
				}
				conn, err = d.DialContext(ctx, network, addr)
			}
			if err != nil {
				return nil, err
			}
			return tracker.track(conn), nil
		},
	}

	tr := &http.Transport{
		DialContext:            hpd.DialContext,
		TLSClientConfig:        &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives:      true,
		MaxIdleConns:           1,
		MaxIdleConnsPerHost:    1,
		MaxConnsPerHost:        1,
		IdleConnTimeout:        300 * time.Millisecond,
		ForceAttemptHTTP2:      false,
		TLSHandshakeTimeout:    timeout / 2,
		ResponseHeaderTimeout:  timeout / 2,
		ExpectContinueTimeout:  100 * time.Millisecond,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 4 * 1024,
		WriteBufferSize:        4 * 1024,
		ReadBufferSize:         4 * 1024,
	}

	rt := countingRoundTripper{base: tr, counter: reqCounter}
	client := &http.Client{Transport: rt, Timeout: timeout}

	info, err := fetchIPInfoWithClient(ctx, client)
	tr.CloseIdleConnections()
	if err != nil && isLikelyPlainHTTPProxy(err) {
		// 回退到HTTP代理测试时，创建新的带超时的context
		newCtx, newCancel := context.WithTimeout(context.Background(), timeout)
		defer newCancel()
		return testHTTPProxy(newCtx, proxyAddr, a, timeout, upstreamDial, reqCounter)
	}
	return info, info.StatusCode, err
}

func testSocks5Proxy(ctx context.Context, proxyAddr string, a Auth, timeout time.Duration,
	upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
	reqCounter *uint64) (IPInfo, int, error) {

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 连接跟踪器
	tracker := newConnTracker()
	defer tracker.closeAll()

	var authSocks *proxy.Auth
	if a.User != "" || a.Pass != "" {
		authSocks = &proxy.Auth{User: a.User, Password: a.Pass}
	}

	baseDialer := newDialer(timeout / 2)
	var forward proxy.Dialer
	if upstreamDial != nil {
		forward = contextDialer{DialContext: upstreamDial}
	} else {
		forward = baseDialer
	}

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
			return tracker.track(conn), nil
		},
		TLSClientConfig:        &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives:      true,
		MaxIdleConns:           1,
		MaxIdleConnsPerHost:    1,
		MaxConnsPerHost:        1,
		IdleConnTimeout:        300 * time.Millisecond,
		ForceAttemptHTTP2:      false,
		TLSHandshakeTimeout:    timeout / 2,
		ResponseHeaderTimeout:  timeout / 2,
		ExpectContinueTimeout:  100 * time.Millisecond,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 4 * 1024,
		WriteBufferSize:        4 * 1024,
		ReadBufferSize:         4 * 1024,
	}

	rt := countingRoundTripper{base: tr, counter: reqCounter}
	client := &http.Client{Transport: rt, Timeout: timeout}

	info, err := fetchIPInfoWithClient(ctx, client)
	tr.CloseIdleConnections()
	return info, info.StatusCode, err
}

// ========== 单次尝试 ==========
func testOne(proxyType string, proxyAddr string, a Auth, timeout time.Duration,
	upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
	reqCounter *uint64) Result {

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	switch proxyType {
	case "http":
		info, _, err := testHTTPProxy(ctx, proxyAddr, a, timeout, upstreamDial, reqCounter)
		if err != nil {
			return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: "HTTP", Success: false, Err: err, StatusCode: info.StatusCode}
		}
		return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: "HTTP", Success: true, StatusCode: info.StatusCode, ISP: info.ISP, IPType: info.IPType, Country: info.Country}
	case "https":
		info, _, err := testHTTPSProxy(ctx, proxyAddr, a, timeout, upstreamDial, reqCounter)
		if err != nil {
			return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: "HTTPS", Success: false, Err: err, StatusCode: info.StatusCode}
		}
		return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: "HTTPS", Success: true, StatusCode: info.StatusCode, ISP: info.ISP, IPType: info.IPType, Country: info.Country}
	case "socks5":
		info, _, err := testSocks5Proxy(ctx, proxyAddr, a, timeout, upstreamDial, reqCounter)
		if err != nil {
			return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: "SOCKS5", Success: false, Err: err, StatusCode: info.StatusCode}
		}
		return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: "SOCKS5", Success: true, StatusCode: info.StatusCode, ISP: info.ISP, IPType: info.IPType, Country: info.Country}
	default:
		return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: strings.ToUpper(proxyType), Success: false, Err: fmt.Errorf("unknown proxy type: %s", proxyType)}
	}
}

// ========== 资源探测（防 OOM / 防 EMFILE） ==========
func detectMemLimitBytes() int64 {
	if s := strings.TrimSpace(os.Getenv("GOMEMLIMIT")); s != "" {
		if v, ok := parseBytes(s); ok && v > 0 {
			return v
		}
	}

	if v := windowsMemLimit(); v > 0 {
		return v
	}
	if b, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		txt := strings.TrimSpace(string(b))
		if txt != "" && txt != "max" {
			if v, err := strconv.ParseInt(txt, 10, 64); err == nil && v > 0 {
				return v
			}
		}
	}
	if b, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil {
		txt := strings.TrimSpace(string(b))
		if v, err := strconv.ParseInt(txt, 10, 64); err == nil && v > 0 && v < 1<<62 {
			return v
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		lines := strings.Split(string(b), "\n")
		for _, ln := range lines {
			if strings.HasPrefix(ln, "MemTotal:") {
				f := strings.Fields(ln)
				if len(f) >= 2 {
					if kb, err := strconv.ParseInt(f[1], 10, 64); err == nil && kb > 0 {
						return kb * 1024
					}
				}
			}
		}
	}
	return 0
}

func parseBytes(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, false
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v, true
	}
	type unit struct {
		suf string
		val int64
	}
	us := []unit{
		{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000}, {"TB", 1000 * 1000 * 1000 * 1000},
		{"KIB", 1024}, {"MIB", 1024 * 1024}, {"GIB", 1024 * 1024 * 1024}, {"TIB", 1024 * 1024 * 1024 * 1024},
	}
	for _, u := range us {
		if strings.HasSuffix(s, u.suf) {
			num := strings.TrimSpace(strings.TrimSuffix(s, u.suf))
			if v, err := strconv.ParseFloat(num, 64); err == nil {
				return int64(v * float64(u.val)), true
			}
		}
	}
	return 0, false
}

func capConcurrency(requested int, unsafe bool) (final int, memLimit int64, fdLimit uint64) {
	memLimit = detectMemLimitBytes()
	fdLimit = detectFDLimit()

	final = requested
	if final <= 0 {
		cpuNum := runtime.NumCPU()
		// 高并发计算
		base := cpuNum * 2000
		if cpuNum >= 8 {
			base = cpuNum * 3000
		}
		if base < 1000 {
			base = 1000
		}
		final = base
	}

	if !unsafe && memLimit > 0 && gcLimitRatio > 0 {
		target := int64(float64(memLimit) * gcLimitRatio)
		debug.SetMemoryLimit(target)
	}

	if !unsafe && fdLimit > 0 {
		const fdPerJob = 4
		maxByFD := int((fdLimit * 70 / 100) / fdPerJob)
		if maxByFD < 1000 {
			maxByFD = 1000
		}
		if final > maxByFD {
			final = maxByFD
		}
	}

	if !unsafe && memLimit > 0 {
		perJob := memPerJobBytes
		if perJob <= 0 {
			perJob = 128 * 1024
		}
		budget := int64(float64(memLimit) * memBudgetRatio)
		if budget <= 0 {
			budget = memLimit / 2
		}
		maxByMem := int(budget / perJob)
		if maxByMem < 1000 {
			maxByMem = 1000
		}
		if final > maxByMem {
			final = maxByMem
		}
	}

	if !unsafe && final < 1000 {
		final = 1000
	}
	return final, memLimit, fdLimit
}

func startMemReclaimer(memLimit int64) {
	if memLimit <= 0 {
		return
	}
	soft := int64(float64(memLimit) * 0.70)
	t := time.NewTicker(3 * time.Second)
	go func() {
		defer t.Stop()
		for range t.C {
			var used int64
			if rss := readProcessRSS(); rss > 0 {
				used = rss
			} else {
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				used = int64(ms.HeapInuse)
			}
			if used > soft {
				debug.FreeOSMemory()
			}
		}
	}()
}

func readProcessRSS() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil || pages <= 0 {
		return 0
	}
	return pages * int64(os.Getpagesize())
}

// 全局紧急暂停标志
var memPaused uint32

// 读取当前进程的TCP连接数（仅Linux）
func readTCPConnCount() int64 {
	// 快速检查 /proc/self/fd 目录下的文件数
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	return int64(len(entries))
}

// 动态并发调节：平衡性能和OOM防护，同时监控TCP连接
func startDynamicLimiter(workers int, memLimit int64, dynamicLimit *int64, active *uint64) {
	if workers <= 0 || dynamicLimit == nil || active == nil {
		return
	}
	hardCap := int64(workers) // 让用户设定或自动计算的并发直接生效
	if hardCap < 256 {
		hardCap = 256
	}
	curLimit := hardCap
	atomic.StoreInt64(dynamicLimit, curLimit)

	// 没有 memLimit 时也要根据 FD 做调节
	if memLimit <= 0 {
		memLimit = 1 << 60 // 约等于 1EiB，确保比例计算有效
	}

	go func() {
		const interval = 200 * time.Millisecond
		var lastGC time.Time
		fdLimit := detectFDLimit()
		if fdLimit == 0 {
			fdLimit = 100000
		}
		fdWarn := int64(float64(fdLimit) * 0.35)
		if fdWarn < 8000 {
			fdWarn = 8000
		}
		if fdWarn > 50000 {
			fdWarn = 50000
		}
		fdHard := int64(float64(fdLimit) * 0.45)
		if fdHard < fdWarn+2000 {
			fdHard = fdWarn + 2000
		}
		if fdHard > 80000 {
			fdHard = 80000
		}
		minLimit := int64(max(128, workers/10))
		if minLimit > int64(workers) {
			minLimit = int64(workers)
		}
		if minLimit < 64 {
			minLimit = 64
		}
		stepUp := int64(max(8, workers/80))

		paused := false
		for {
			time.Sleep(interval)

			// 检查内存
			var used int64
			if rss := readProcessRSS(); rss > 0 {
				used = rss
			} else {
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				used = int64(ms.HeapAlloc)
			}
			usedRatio := float64(used) / float64(memLimit)

			// 检查FD/连接数
			fdCount := readTCPConnCount()
			fdRatio := float64(fdCount) / float64(fdLimit)
			fdResume := fdWarn * 8 / 10
			if fdResume < 4000 {
				fdResume = 4000
			}

			// 动态调整并发：过高时快速下调，安全时缓慢上调
			shouldPause := false
			shouldGC := false

			if fdCount > fdHard {
				curLimit = minLimit // 硬触发直接落到最低并发，快速回收
				paused = true
				shouldGC = true
			} else if fdCount > fdWarn {
				curLimit = max64(minLimit, curLimit*8/10)
				if time.Since(lastGC) > 300*time.Millisecond {
					shouldGC = true
				}
			} else if usedRatio > 0.88 || fdRatio > 0.85 {
				curLimit = max64(minLimit, curLimit*7/10)
				shouldPause = true
				shouldGC = true
			} else if usedRatio > 0.80 || fdRatio > 0.80 {
				curLimit = max64(minLimit, curLimit*8/10)
				shouldGC = true
			} else if usedRatio > 0.70 || fdRatio > 0.70 {
				curLimit = max64(minLimit, curLimit*9/10)
				if time.Since(lastGC) > 300*time.Millisecond {
					shouldGC = true
				}
			} else if usedRatio > 0.60 || fdRatio > 0.60 {
				if time.Since(lastGC) > time.Second {
					debug.FreeOSMemory()
					lastGC = time.Now()
				}
			} else {
				// 低压：逐步回升并发
				if curLimit < hardCap {
					curLimit += stepUp
					if curLimit > hardCap {
						curLimit = hardCap
					}
				}
			}

			// FD 解除暂停需要明显下降，避免抖动
			if paused {
				atomic.StoreUint32(&memPaused, 1)
				if fdCount < fdResume && usedRatio < 0.65 {
					paused = false
				}
			} else if shouldPause {
				atomic.StoreUint32(&memPaused, 1)
				paused = true
			} else if fdCount < fdResume && usedRatio < 0.65 {
				atomic.StoreUint32(&memPaused, 0)
			}

			atomic.StoreInt64(dynamicLimit, curLimit)

			if shouldGC && time.Since(lastGC) > 100*time.Millisecond {
				runtime.GC()
				debug.FreeOSMemory()
				lastGC = time.Now()
			}
		}
	}()
}

// ========== 进度输出 ==========
func formatETA(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// ========== 输出格式 ==========
func nonEmpty(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func resultToLine(r Result) string {
	var scheme string
	switch strings.ToUpper(r.ProxyType) {
	case "SOCKS5":
		scheme = "socks5"
	case "HTTP":
		scheme = "http"
	case "HTTPS":
		scheme = "https"
	default:
		scheme = "http"
	}

	isp := nonEmpty(r.ISP, "-")
	ipType := nonEmpty(r.IPType, "-")
	country := nonEmpty(r.Country, "-")

	u := &url.URL{
		Scheme: scheme,
		Host:   r.ProxyAddr,
	}
	if r.Auth.User != "" || r.Auth.Pass != "" {
		u.User = url.UserPassword(r.Auth.User, r.Auth.Pass)
	}
	return fmt.Sprintf("%s#[%s][%s][%s]\n", u.String(), isp, ipType, country)
}

// ========== worker ==========
type Outcome struct {
	ProxyAddr string
	Successes []Result
	FailErr   error
	FailWhy   string
}

func worker(
	wg *sync.WaitGroup,
	jobs <-chan Job,
	out chan<- Outcome,
	auths []Auth,
	timeout time.Duration,
	mode string,
	delay time.Duration,
	upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
	reqCounter *uint64,
	active *uint64,
	dynLimit *int64,
) {
	defer wg.Done()

	mode = strings.ToLower(mode)

	for job := range jobs {
		// 当动态并发或暂停信号触发时，阻塞等待
		for {
			if atomic.LoadUint32(&memPaused) == 1 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if dynLimit == nil || active == nil {
				break
			}
			dl := atomic.LoadInt64(dynLimit)
			if dl <= 0 {
				break
			}
			if atomic.LoadUint64(active) < uint64(dl) {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}

		if active != nil {
			atomic.AddUint64(active, 1)
		}
		authList := make([]Auth, 0, len(auths)+1)
		authList = append(authList, Auth{})
		if job.InlineAuth != nil {
			if job.InlineAuth.User != "" || job.InlineAuth.Pass != "" {
				authList = append(authList, *job.InlineAuth)
			}
		} else {
			for _, a := range auths {
				if a.User == "" && a.Pass == "" {
					continue
				}
				authList = append(authList, a)
			}
		}

		var types []string
		switch mode {
		case "http":
			types = []string{"http"}
		case "https":
			types = []string{"https"}
		case "socks5", "s5":
			types = []string{"socks5"}
		case "all":
			types = guessProxyOrderWithScheme(job.ProxyAddr, job.SchemeHint)
		case "auto":
			types = guessProxyOrderWithScheme(job.ProxyAddr, job.SchemeHint)
		default:
			types = guessProxyOrderWithScheme(job.ProxyAddr, job.SchemeHint)
		}

		var successes []Result
		var reasons []string
		var lastErr error
		var ipUnreachable bool // 标记IP是否不可达

		for _, tp := range types {
			var okThisType bool

			// 如果IP已经被标记为不可达，直接跳过后续所有测试
			if ipUnreachable {
				break
			}

			for _, a := range authList {
				res := testOne(tp, job.ProxyAddr, a, timeout, upstreamDial, reqCounter)
				if res.Success {
					okThisType = true
					successes = append(successes, res)
					break
				}
				lastErr = res.Err
				errClass := classifyErr(res.Err)
				reasons = append(reasons, errClass)

				// 检查是否是不可达错误（RST或网络不可达等），如果是则标记并跳过后续测试
				if errClass == "reset" || errClass == "unreachable" || errClass == "refused" {
					ipUnreachable = true
					break
				}
			}

			if mode == "auto" && okThisType {
				break
			}
		}

		why := ""
		if len(successes) == 0 {
			why = choosePrimaryReason(reasons, lastErr)
		}

		out <- Outcome{
			ProxyAddr: job.ProxyAddr,
			Successes: successes,
			FailErr:   lastErr,
			FailWhy:   why,
		}

		if delay > 0 {
			time.Sleep(delay)
		}
		if active != nil {
			atomic.AddUint64(active, ^uint64(0))
		}
	}
}

func choosePrimaryReason(reasons []string, lastErr error) string {
	priority := []string{
		"auth",
		"ipinfo",
		"non204",
		"connect_fail",
		"tls",
		"timeout",
		"refused",
		"unreachable",
		"reset",
		"dial",
		"dns",
		"eof",
		"other",
	}
	count := make(map[string]int, 16)
	for _, r := range reasons {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		count[r]++
	}
	for _, p := range priority {
		if count[p] > 0 {
			return p
		}
	}
	if lastErr != nil {
		return classifyErr(lastErr)
	}
	return "other"
}

// ========== 统计总行数（流式） ==========
func countWorkItems(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var n int64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 128*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n++
	}
	return n, sc.Err()
}

func main() {
	log.SetFlags(0)

	ipFile := flag.String("ip", "", "代理列表文件（每行一个：IP / host:port / URL / user:pass@host:port）")
	portP := flag.String("p", "", "当输入行为纯 IP / 无端口 host 时使用的端口（例如 443）")
	portLong := flag.String("port", "", "同 -p（兼容）")
	outFile := flag.String("out", "", "输出文件（仅写入成功项）；留空自动生成")
	modeFlag := flag.String("mode", "auto", "测试模式：http/https/socks5/all/auto（auto=测到成功就停；all=每种类型都测）")
	authFile := flag.String("auth", "", "可选：认证文件 user:pass（每行一个）；留空=不带认证")
	timeout := flag.Duration("timeout", defaultTimeout, "单次测试超时（例如 10s）")
	delay := flag.Duration("delay", 0, "每个 IP 处理完成后的延迟（例如 10ms）")
	concurrency := flag.Int("c", 0, "并发（0=自动按资源估算；会防 OOM/EMFILE）")
	progressEvery := flag.Duration("progress", 1*time.Second, "进度输出间隔（例如 1s）")
	verbose := flag.Bool("v", false, "输出失败详情（默认不输出）")
	memBudgetFlag := flag.Float64("mem-budget", 0.55, "自动并发内存预算比例（0-1，设大可提高并发）")
	memPerJobFlag := flag.Int("mem-per-job", 256*1024, "自动并发单任务预估字节（设小可提高并发）")
	gcLimitFlag := flag.Float64("gc-limit", 0.75, "GC 内存上限比例（0=不设，设大可提高并发但更易 OOM）")
	unsafeFlag := flag.Bool("unsafe", false, "解除内存/FD/动态并发等安全限制（风险自担）")

	skipCDN := flag.Bool("skip-cdn", true, "自动跳过 CDN IP 段（联网获取）")
	upstreamAddr := flag.String("upstream", "", "可选：上游代理 host:port")
	upstreamMode := flag.String("upstream-mode", "s5", "上游代理协议：s5/http/https")
	upstreamAuthStr := flag.String("upstream-auth", "", "可选：上游认证 user:pass")

	flag.Parse()
	if *memBudgetFlag > 0 && *memBudgetFlag <= 1 {
		memBudgetRatio = *memBudgetFlag
	}
	if *memPerJobFlag > 0 {
		memPerJobBytes = int64(*memPerJobFlag)
	}
	if *gcLimitFlag <= 0 {
		gcLimitRatio = 0
	} else {
		gcLimitRatio = *gcLimitFlag
		if gcLimitRatio > 1 {
			gcLimitRatio = 1
		}
	}

	if strings.TrimSpace(*ipFile) == "" {
		log.Println("必须提供 -ip")
		flag.Usage()
		os.Exit(2)
	}

	if *portP == "" && *portLong != "" {
		*portP = *portLong
	}

	mode := strings.ToLower(strings.TrimSpace(*modeFlag))
	switch mode {
	case "http", "https", "socks5", "s5", "all", "auto":
	default:
		log.Fatalf("无效的 -mode=%s，应为 http/https/socks5/all/auto", mode)
	}

	// 当未指定端口时，从 mode 推断默认端口
	if *portP == "" {
		switch mode {
		case "https":
			*portP = "443"
		case "http":
			*portP = "80"
		case "socks5", "s5":
			*portP = "1080"
		}
	}

	if *outFile == "" {
		ts := time.Now().Format("20060102-150405")
		p := *portP
		if p == "" {
			p = "auto"
		}
		*outFile = fmt.Sprintf("result_mode-%s_port-%s_%s.txt", mode, p, ts)
	}

	total, err := countWorkItems(*ipFile)
	if err != nil {
		log.Fatalf("统计 IP 行数失败: %v", err)
	}
	if total == 0 {
		log.Fatalf("IP 文件为空或全是注释/空行")
	}

	auths, err := loadAuthsOptional(*authFile)
	if err != nil {
		log.Fatalf("加载 auth 文件失败: %v", err)
	}

	var cdn *CDNFilter
	if *skipCDN {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cdn, err = loadCDNFilter(ctx)
		if err != nil {
			log.Printf("CDN 列表获取失败，将不跳过 CDN：%v", err)
			cdn = nil
		} else {
			log.Printf("CDN 列表已加载：v4=%d v6=%d（cloudflare/fastly/cloudfront）", len(cdn.V4), len(cdn.V6))
		}
	}

	var upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error)
	if *upstreamAddr != "" {
		var ua Auth
		if *upstreamAuthStr != "" {
			parts := strings.SplitN(*upstreamAuthStr, ":", 2)
			if len(parts) == 2 {
				ua.User, ua.Pass = parts[0], parts[1]
			}
		}
		upstreamDial, err = buildUpstreamDialer(*upstreamMode, *upstreamAddr, ua, *timeout)
		if err != nil {
			log.Fatalf("创建上游代理失败: %v", err)
		}
	}

	workers, memLimit, fdLimit := capConcurrency(*concurrency, *unsafeFlag)

	var (
		done    uint64
		okIP    uint64
		okLine  uint64
		fail    uint64
		skip    uint64
		reqCnt  uint64
		activeW uint64
		dynLim  int64
	)

	if !*unsafeFlag {
		startMemReclaimer(memLimit)
		atomic.StoreInt64(&dynLim, int64(workers))
		startDynamicLimiter(workers, memLimit, &dynLim, &activeW)
	}

	log.Printf("start: ips=%d mode=%s timeout=%s concurrency=%d memLimit=%s fdLimit=%d out=%s unsafe=%v",
		total, mode, timeout.String(), workers, humanBytes(memLimit), fdLimit, *outFile, *unsafeFlag)

	out, err := os.Create(*outFile)
	if err != nil {
		log.Fatalf("创建输出文件失败(%s): %v", *outFile, err)
	}
	defer out.Close()
	writer := bufio.NewWriterSize(out, 512*1024)
	defer writer.Flush()

	failReasons := &CounterMap{}
	skipReasons := &CounterMap{}

	jobQueueSize := min(max(128, workers/4), 4096)
	jobs := make(chan Job, jobQueueSize)
	outcomes := make(chan Outcome, jobQueueSize)

	stopProg := make(chan struct{})
	go func() {
		t := time.NewTicker(*progressEvery)
		defer t.Stop()
		start := time.Now()
		var lastDone, lastReq uint64
		var emaIPS, emaQPS float64

		for {
			select {
			case <-stopProg:
				return
			case <-t.C:
				d := atomic.LoadUint64(&done)
				r := atomic.LoadUint64(&reqCnt)
				okip := atomic.LoadUint64(&okIP)
				fc := atomic.LoadUint64(&fail)
				sc := atomic.LoadUint64(&skip)

				deltaDone := d - lastDone
				deltaReq := r - lastReq
				curIPS := float64(deltaDone) / progressEvery.Seconds()
				curQPS := float64(deltaReq) / progressEvery.Seconds()

				if emaIPS == 0 {
					emaIPS = curIPS
				} else {
					emaIPS = emaIPS*0.80 + curIPS*0.20
				}
				if emaQPS == 0 {
					emaQPS = curQPS
				} else {
					emaQPS = emaQPS*0.80 + curQPS*0.20
				}

				lastDone = d
				lastReq = r

				left := total - int64(d)
				if left < 0 {
					left = 0
				}
				var eta time.Duration
				if emaIPS > 0 {
					eta = time.Duration(float64(left) / emaIPS * float64(time.Second))
				}

				dynCur := atomic.LoadInt64(&dynLim)
				actCur := atomic.LoadUint64(&activeW)
				fmt.Fprintf(os.Stderr,
					"ips:%9d/%-9d left:%-9d ip/s:%8.1f qps:%8.1f eta:%-10s ok:%-6d fail:%-8d skip:%-6d dyn:%-6d act:%-6d up:%s\n",
					d, total, left, emaIPS, emaQPS, formatETA(eta), okip, fc, sc, dynCur, actCur, formatETA(time.Since(start)),
				)
			}
		}
	}()

	var writeWg sync.WaitGroup
	writeWg.Add(1)
	go func() {
		defer writeWg.Done()
		flushTicker := time.NewTicker(500 * time.Millisecond)
		defer flushTicker.Stop()

		pending := 0
		for {
			select {
			case oc, ok := <-outcomes:
				if !ok {
					_ = writer.Flush()
					return
				}

				if len(oc.Successes) > 0 {
					atomic.AddUint64(&okIP, 1)
					atomic.AddUint64(&okLine, uint64(len(oc.Successes)))
					for _, r := range oc.Successes {
						if _, err := writer.WriteString(resultToLine(r)); err != nil {
							log.Printf("write fail: %v", err)
						} else {
							pending++
							if pending >= 256 {
								_ = writer.Flush()
								pending = 0
							}
						}
					}
				} else {
					atomic.AddUint64(&fail, 1)
					failReasons.Inc(oc.FailWhy)
					if *verbose && oc.FailErr != nil {
						log.Printf("FAIL %s why=%s err=%v", oc.ProxyAddr, oc.FailWhy, oc.FailErr)
					}
				}

				atomic.AddUint64(&done, 1)

			case <-flushTicker.C:
				_ = writer.Flush()
				pending = 0
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go worker(&wg, jobs, outcomes, auths, *timeout, mode, *delay, upstreamDial, &reqCnt, &activeW, &dynLim)
	}

	go func() {
		defer close(jobs)

		f, err := os.Open(*ipFile)
		if err != nil {
			log.Printf("open ip file failed: %v", err)
			return
		}
		defer f.Close()

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

		for sc.Scan() {
			raw := strings.TrimSpace(sc.Text())
			if raw == "" || strings.HasPrefix(raw, "#") {
				continue
			}

			addr, schemeHint, inlineAuth, err := parseProxyLine(raw, *portP)
			if err != nil {
				atomic.AddUint64(&skip, 1)
				skipReasons.Inc("bad_line")
				atomic.AddUint64(&done, 1)
				continue
			}

			if cdn != nil {
				host := hostFromHostPort(addr)
				ip := net.ParseIP(strings.Trim(host, "[]"))
				if ip != nil {
					if provider, ok := cdn.Match(ip); ok {
						atomic.AddUint64(&skip, 1)
						skipReasons.Inc("cdn_" + provider)
						atomic.AddUint64(&done, 1)
						continue
					}
				}
			}

			jobs <- Job{
				ProxyAddr:  addr,
				SchemeHint: schemeHint,
				InlineAuth: inlineAuth,
				RawLine:    raw,
			}
		}
		if err := sc.Err(); err != nil {
			log.Printf("scan ip file error: %v", err)
		}
	}()

	wg.Wait()
	close(outcomes)
	writeWg.Wait()
	close(stopProg)

	_ = writer.Flush()
	fmt.Fprintf(os.Stderr, "done. out=%s okIP=%d okLines=%d fail=%d skip=%d\n",
		*outFile,
		atomic.LoadUint64(&okIP),
		atomic.LoadUint64(&okLine),
		atomic.LoadUint64(&fail),
		atomic.LoadUint64(&skip),
	)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func humanBytes(b int64) string {
	if b <= 0 {
		return "unknown"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	suffix := []string{"KiB", "MiB", "GiB", "TiB"}
	if exp >= len(suffix) {
		exp = len(suffix) - 1
	}
	return fmt.Sprintf("%.1f%s", float64(b)/float64(div), suffix[exp])
}
