//go:build !windows
// +build !windows

package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"
)

// ============================================================
// 🔥 改进版本：针对Linux内存问题的完整修复
// ============================================================

// 缓冲区池：复用bufio.Reader
var bufReaderPool = sync.Pool{
	New: func() interface{} {
		return bufio.NewReaderSize(nil, 1024)
	},
}

// 获取缓冲读取器
func getBufReader(r io.Reader) *bufio.Reader {
	br := bufReaderPool.Get().(*bufio.Reader)
	br.Reset(r)
	return br
}

// 返回缓冲读取器到池
func putBufReader(br *bufio.Reader) {
	bufReaderPool.Put(br)
}

// ============================================================
// 改进的HTTPProxyDialer：处理TLS握手和缓冲区释放
// ============================================================
type HTTPProxyDialerImproved struct {
	addr     string
	auth     *Auth
	useTLS   bool
	timeout  time.Duration
	baseDial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (d *HTTPProxyDialerImproved) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var conn net.Conn
	var err error

	if d.baseDial != nil {
		conn, err = d.baseDial(ctx, "tcp", d.addr)
	} else {
		// 🔥 禁用 KeepAlive
		nd := &net.Dialer{Timeout: d.timeout, KeepAlive: -1}
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
	} else {
		_ = conn.SetDeadline(time.Now().Add(d.timeout))
	}

	if d.useTLS {
		host, _, splitErr := net.SplitHostPort(d.addr)
		if splitErr != nil || host == "" {
			host = d.addr
		}

		// 🔥 TLS握手超时控制
		hsCtx, hsCancel := context.WithTimeout(ctx, d.timeout)
		defer hsCancel()

		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS10,
			MaxVersion:         tls.VersionTLS12,
		})

		// 🔥 在goroutine中进行握手，允许cancel
		done := make(chan error, 1)
		go func() {
			done <- tlsConn.Handshake()
		}()

		select {
		case hsErr := <-done:
			if hsErr != nil {
				_ = tlsConn.Close()
				_ = conn.Close()
				return nil, hsErr
			}
		case <-hsCtx.Done():
			_ = tlsConn.Close()
			_ = conn.Close()
			return nil, fmt.Errorf("tls handshake timeout")
		}

		conn = tlsConn
	}

	// 🔥 使用缓冲区池处理响应头
	br := getBufReader(conn)
	defer putBufReader(br)

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

	// 读取响应头直到空行
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

// ============================================================
// 改进的 SOCKS5 测试函数
// ============================================================
func testSocks5ProxyImproved(ctx context.Context, proxyAddr string, a Auth, timeout time.Duration,
	upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
	reqCounter *uint64) (IPInfo, int, error) {

	var forward proxy.Dialer
	if upstreamDial != nil {
		forward = contextDialer{DialContext: upstreamDial}
	} else {
		// 🔥 禁用 KeepAlive，减少后台 goroutine
		forward = &net.Dialer{Timeout: timeout, KeepAlive: -1}
	}

	var authSocks *proxy.Auth
	if a.User != "" || a.Pass != "" {
		authSocks = &proxy.Auth{User: a.User, Password: a.Pass}
	}

	// 🔥 拨号超时控制
	dialCtx, dialCancel := context.WithTimeout(ctx, timeout+1*time.Second)
	defer dialCancel()

	dialer, err := proxy.SOCKS5("tcp", proxyAddr, authSocks, forward)
	if err != nil {
		return IPInfo{}, 0, err
	}

	// 🔥 确保 SOCKS5 dialer 中的 goroutine 被清理
	defer func() {
		// 等待任何待处理的操作完成
		select {
		case <-dialCtx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}()

	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.Dial(network, addr)
			if err != nil {
				return nil, err
			}
			// 设置deadline防止连接泄漏
			if deadline, ok := ctx.Deadline(); ok {
				_ = conn.SetDeadline(deadline)
			}
			return conn, nil
		},
		DisableKeepAlives:      true,
		MaxIdleConns:           0,
		MaxIdleConnsPerHost:    0,
		MaxConnsPerHost:        1,
		IdleConnTimeout:        1 * time.Millisecond, // 🔥 极短超时
		ForceAttemptHTTP2:      false,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		ExpectContinueTimeout:  500 * time.Millisecond,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 1 * 1024, // 🔥 减小
		WriteBufferSize:        1 * 1024, // 🔥 减小
		ReadBufferSize:         1 * 1024, // 🔥 减小
	}

	// 🔥 双重清理确保所有连接关闭
	defer func() {
		tr.CloseIdleConnections()
		time.Sleep(5 * time.Millisecond)
		tr.CloseIdleConnections()
	}()

	rt := countingRoundTripper{base: tr, counter: reqCounter}
	client := &http.Client{Transport: rt, Timeout: timeout}

	info, err := fetchIPInfoWithClient(ctx, client)
	return info, info.StatusCode, err
}

// ============================================================
// 改进的 HTTP 测试函数
// ============================================================
func testHTTPProxyImproved(ctx context.Context, proxyAddr string, a Auth, timeout time.Duration,
	upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
	reqCounter *uint64) (IPInfo, int, error) {

	proxyURL := (&url.URL{Scheme: "http", Host: proxyAddr})
	if a.User != "" || a.Pass != "" {
		proxyURL.User = url.UserPassword(a.User, a.Pass)
	}

	tr := &http.Transport{
		Proxy:                  http.ProxyURL(proxyURL),
		TLSClientConfig:        &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives:      true,
		MaxIdleConns:           0,
		MaxIdleConnsPerHost:    0,
		MaxConnsPerHost:        1,
		IdleConnTimeout:        1 * time.Millisecond, // 🔥 极短超时
		ForceAttemptHTTP2:      false,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		ExpectContinueTimeout:  1 * time.Second,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 2 * 1024,
		WriteBufferSize:        1 * 1024, // 🔥 减小
		ReadBufferSize:         1 * 1024, // 🔥 减小
	}

	if a.User != "" || a.Pass != "" {
		h := make(http.Header)
		cred := base64.StdEncoding.EncodeToString([]byte(a.User + ":" + a.Pass))
		h.Set("Proxy-Authorization", "Basic "+cred)
		tr.ProxyConnectHeader = h
	}

	if upstreamDial != nil {
		tr.DialContext = upstreamDial
	}

	defer func() {
		tr.CloseIdleConnections()
		time.Sleep(5 * time.Millisecond)
		tr.CloseIdleConnections()
	}()

	rt := countingRoundTripper{base: tr, counter: reqCounter}
	client := &http.Client{Transport: rt, Timeout: timeout}

	info, err := fetchIPInfoWithClient(ctx, client)
	return info, info.StatusCode, err
}

// ============================================================
// 改进的 HTTPS 测试函数
// ============================================================
func testHTTPSProxyImproved(ctx context.Context, proxyAddr string, a Auth, timeout time.Duration,
	upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
	reqCounter *uint64) (IPInfo, int, error) {

	var cred *Auth
	if a.User != "" || a.Pass != "" {
		cred = &a
	}
	hpd := &HTTPProxyDialerImproved{
		addr:     proxyAddr,
		auth:     cred,
		useTLS:   true,
		timeout:  timeout,
		baseDial: upstreamDial,
	}

	tr := &http.Transport{
		DialContext:            hpd.DialContext,
		TLSClientConfig:        &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives:      true,
		MaxIdleConns:           0,
		MaxIdleConnsPerHost:    0,
		MaxConnsPerHost:        1,
		IdleConnTimeout:        1 * time.Millisecond, // 🔥 极短超时
		ForceAttemptHTTP2:      false,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		ExpectContinueTimeout:  1 * time.Second,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 2 * 1024,
		WriteBufferSize:        1 * 1024, // 🔥 减小
		ReadBufferSize:         1 * 1024, // 🔥 减小
	}

	defer func() {
		tr.CloseIdleConnections()
		time.Sleep(5 * time.Millisecond)
		tr.CloseIdleConnections()
	}()

	rt := countingRoundTripper{base: tr, counter: reqCounter}
	client := &http.Client{Transport: rt, Timeout: timeout}

	info, err := fetchIPInfoWithClient(ctx, client)
	return info, info.StatusCode, err
}

// ============================================================
// 改进的 testOne 函数，使用改进的测试函数
// ============================================================
func testOneImproved(proxyType string, proxyAddr string, a Auth, timeout time.Duration,
	upstreamDial func(ctx context.Context, network, addr string) (net.Conn, error),
	reqCounter *uint64) Result {

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	switch proxyType {
	case "http":
		info, _, err := testHTTPProxyImproved(ctx, proxyAddr, a, timeout, upstreamDial, reqCounter)
		if err != nil {
			return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: "HTTP", Success: false, Err: err, StatusCode: info.StatusCode}
		}
		return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: "HTTP", Success: true, StatusCode: info.StatusCode, ISP: info.ISP, IPType: info.IPType, Country: info.Country}
	case "https":
		info, _, err := testHTTPSProxyImproved(ctx, proxyAddr, a, timeout, upstreamDial, reqCounter)
		if err != nil {
			return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: "HTTPS", Success: false, Err: err, StatusCode: info.StatusCode}
		}
		return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: "HTTPS", Success: true, StatusCode: info.StatusCode, ISP: info.ISP, IPType: info.IPType, Country: info.Country}
	case "socks5":
		info, _, err := testSocks5ProxyImproved(ctx, proxyAddr, a, timeout, upstreamDial, reqCounter)
		if err != nil {
			return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: "SOCKS5", Success: false, Err: err, StatusCode: info.StatusCode}
		}
		return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: "SOCKS5", Success: true, StatusCode: info.StatusCode, ISP: info.ISP, IPType: info.IPType, Country: info.Country}
	default:
		return Result{ProxyAddr: proxyAddr, Auth: a, ProxyType: strings.ToUpper(proxyType), Success: false, Err: fmt.Errorf("unknown proxy type: %s", proxyType)}
	}
}

// ============================================================
// 改进的动态限制器：基于 RSS 而非 HeapAlloc
// ============================================================
func startDynamicLimiterImproved(workers int, memLimit int64, dynamicLimit *int64, active *uint64) {
	if workers <= 0 || dynamicLimit == nil || active == nil {
		return
	}
	atomic.StoreInt64(dynamicLimit, int64(workers))

	if memLimit <= 0 {
		return
	}

	go func() {
		const interval = 100 * time.Millisecond
		var lastGCTime time.Time

		for {
			time.Sleep(interval)

			// 🔥 优先使用 RSS，fallback 到 HeapAlloc
			rss := readProcessRSS()
			var usedRatio float64
			if rss > 0 {
				usedRatio = float64(rss) / float64(memLimit)
			} else {
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				usedRatio = float64(ms.HeapAlloc) / float64(memLimit)
			}

			cur := atomic.LoadInt64(dynamicLimit)
			newLimit := cur

			// 🔥 更激进的阈值和调整
			if usedRatio > 0.80 {
				// 危急状态：大幅降低并发
				atomic.StoreUint32(&memPaused, 1)
				newLimit = cur / 8
				if newLimit < 2 {
					newLimit = 2
				}
				debug.FreeOSMemory()
				runtime.GC()
				log.Printf("🔥 内存告急 (%.1f%%), 并发降至 %d", usedRatio*100, newLimit)
			} else if usedRatio > 0.70 {
				atomic.StoreUint32(&memPaused, 0)
				newLimit = cur / 4
				if newLimit < 4 {
					newLimit = 4
				}
				debug.FreeOSMemory()
			} else if usedRatio > 0.60 {
				newLimit = cur / 2
				if newLimit < 8 {
					newLimit = 8
				}
			} else if usedRatio > 0.50 {
				newLimit = cur - cur/3
			} else if usedRatio < 0.30 && time.Since(lastGCTime) > 2*time.Second {
				// 内存充足：逐步增加并发
				newLimit = cur + cur/6
			} else if usedRatio < 0.20 {
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

			// 定期主动GC
			if time.Since(lastGCTime) > 1*time.Second && usedRatio > 0.40 {
				debug.FreeOSMemory()
				runtime.GC()
				lastGCTime = time.Now()
			}
		}
	}()
}

// ============================================================
// 改进的内存回收器
// ============================================================
func startMemReclaimerImproved(memLimit int64) {
	if memLimit <= 0 {
		return
	}

	// 🔥 基于 RSS 的更激进策略
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for range ticker.C {
			rss := readProcessRSS()
			if rss == 0 {
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				rss = int64(ms.HeapAlloc)
			}

			// 当 RSS 达到限制的 60% 时主动释放
			if rss > int64(float64(memLimit)*0.60) {
				debug.FreeOSMemory()
				runtime.GC()
			}
		}
	}()
}
