## 一、程序整体说明

这个程序的功能：**批量测试代理（HTTP / HTTPS / SOCKS5），并输出可用代理列表 + IP 情报**  

- 从 IP 文件中读取待测代理（IP 或 IP:Port）  
- 从 auth 文件中读取账号密码（`user:pass`）  
- 对每个 (IP, 账号) 组合进行测试  
- 通过线上 IP API 检测代理是否真正出网成功 + 获取 ISP / 类型 / 国家  
- 将成功的代理输出为：  

```text
socks5://user:pass@1.2.3.4:1080#[ISP][IPType][Country]
```

---

## 二、输入文件格式

### 1. IP 文件（`-ip`，默认 `ip.txt`）

每行一个目标，可以是：

1. 直接写 `ip:port`（推荐）

```text
# 示例 ip.txt
127.0.0.1:1080
192.168.0.10:8080
10.0.0.2:3128
```

2. 只写 IP（IPv4 / IPv6），端口用 `-port` 统一指定：

```text
# 示例 ip_pure.txt
127.0.0.1
192.168.0.10
2001:4860:4860::8888
```

配合：

```bash
-port 1080
```

程序会自动拼成：

- `127.0.0.1:1080`  
- `192.168.0.10:1080`  
- `[2001:4860:4860::8888]:1080`  

> ⚠ 默认只允许「本地 / 内网 IP」，如果你要测公网代理，必须加 `-allow-nonlocal`，否则会被跳过。

---

### 2. 账号密码文件（`-auth`，默认 `auth.txt`）

每行一个 `user:pass`，会跟 IP 文件做**笛卡尔积**：

```text
# 示例 auth.txt
user1:pass1
user2:pass2
user3:pass3
```

如果 IP 文件有 2 行，auth 有 3 行，就会生成 2 × 3 = 6 个任务。

---

## 三、参数总览（带解释）

| 参数名           | 类型       | 默认值                | 说明 |
|------------------|------------|-----------------------|------|
| `-ip`            | string     | `ip.txt`              | 待测代理 IP 列表文件 |
| `-auth`          | string     | `auth.txt`            | 账号密码文件（`user:pass`） |
| `-port`          | string     | 空                    | **仅当 IP 文件是纯 IP 时**使用的统一端口 |
| `-mode`          | string     | `auto`                | 测试协议：`s5` / `http` / `https` / `auto` |
| `-allow-nonlocal`| bool       | `false`               | 是否允许测试非本地/非内网 IP |
| `-out`           | string     | 自动生成              | 结果输出文件路径（只写成功代理） |
| `-threads`       | int        | `0`                   | worker 数，`0` = 自动（CPU 核 × 2000，最少 4） |
| `-delay`         | duration   | `10ms`                | 每个代理测完后的 sleep，用于减低 CPU / QPS |
| `-upstream`      | string     | 空                    | 上游代理地址 `host:port`，可选 |
| `-upstream-mode` | string     | `s5`                  | 上游协议：`s5` / `http` / `https` |
| `-upstream-auth` | string     | 空                    | 上游认证 `user:pass` |

---

## 四、核心输出格式说明

成功时（`r.Success == true`）日志与文件内容类似：

### 控制台输出

```text
[OK]   socks5://user1:pass1@127.0.0.1:1080#[SomeISP][residential][US]
```

### 文件内一行

```text
socks5://user1:pass1@127.0.0.1:1080#[SomeISP][residential][US]
```

- 协议：根据实际探测结果为 `socks5` / `http` / `https`  
- ISP：来自 IP API 的 `ISP` 字段  
- IPType：来自 `type` 或 `use_type` 等字段（如 `residential`, `hosting` 等）  
- Country：主接口为国家代码（如 `US`），备用接口为国家名（如 `United States`）  

失败时会打印：

```text
[FAIL] 127.0.0.1:1080  auth=user1:pass1  err=dial tcp ...
```

不会写入文件。

---

## 五、执行示例（覆盖所有参数的常用组合）

我用几个场景把所有参数都给你带到一遍 ( •̀ ω •́ )✧  

### 场景 1：**本地内网 SOCKS5 代理，IP 已含端口，自动模式**

- 使用默认 `ip.txt` / `auth.txt`  
- 不带 `-port`（因为 `ip.txt` 里已经是 `ip:port`）  
- `mode=auto`：根据端口智能尝试 HTTP/SOCKS5  

#### 示例命令

```bash
go run main.go \
  -ip ip.txt \
  -auth auth.txt \
  -mode auto
```

#### 说明

- 默认只会测试**本地 / 内网** IP（10.0.0.0/8, 192.168.0.0/16 等）  
- 若 `ip.txt` 有 `127.0.0.1:1080`，会优先按 SOCKS5 测  
- 输出结果文件类似：`result_mode-auto_port-_20250101-120000.txt`  

---

### 场景 2：**本地 SOCKS5 代理，IP 文件只有 IP，不含端口**

- IP 文件：`ip_pure.txt`（只写 IP）  
- 用 `-port 1080` 拼端口  
- 明确指定 `-mode s5`

#### 示例命令

```bash
go run main.go \
  -ip ip_pure.txt \
  -auth auth.txt \
  -port 1080 \
  -mode s5
```

#### 说明

- `makeProxyAddr` 会把每行 IP 拼成 `ip:1080`  
- 只按 SOCKS5 协议去连（不会尝试 HTTP）  
- 适合测试一批统一端口的 SOCKS5 代理  

---

### 场景 3：**测试公网 HTTP 代理（已获授权），显式允许非本地**

这里开始用到了 `-allow-nonlocal` 参数 (认真脸 ✧)  

- IP 文件含公网 IP：`proxy_public_http.txt`：  

```text
203.0.113.10:8080
198.51.100.23:3128
```

- 你已确保：  
  - 对方明确授权你测试这些代理  
  - 不会触犯当地法律与对方服务条款  

#### 示例命令

```bash
go run main.go \
  -ip proxy_public_http.txt \
  -auth auth.txt \
  -mode http \
  -allow-nonlocal
```

#### 说明

- 如果不加 `-allow-nonlocal`，程序会直接跳过这些行并记录 log：  

  > 检测到非本地/内网 IP(...)，默认跳过。如已获授权，请增加 -allow-nonlocal  

- `mode=http`：只以 HTTP 代理方式测试（CONNECT + 直连）  

---

### 场景 4：**自定义输出文件名 + 控制并发 threads + 调整 delay**

这一组把 `-out` / `-threads` / `-delay` 都用上了。  

#### 示例命令

```bash
go run main.go \
  -ip ip.txt \
  -auth auth.txt \
  -mode auto \
  -allow-nonlocal \
  -out good_proxies.txt \
  -threads 500 \
  -delay 50ms
```

#### 说明

- `-out good_proxies.txt`：不会用自动文件名，而是固定写入这个文件（覆盖写入）  
- `-threads 500`：启动 500 个 worker 并发测试  
  - 如果 `-threads=0` 或不写：自动 = CPU核数 × 2000（非常激进）  
  - 建议公网环境 & 外网 API 接口时**适当调小**，避免 QPS 过高 ⚠  
- `-delay 50ms`：每个任务测完后 sleep 50ms，平滑 CPU / 网络压力  

---

### 场景 5：**通过 SOCKS5 上游代理转发，再测试下一层代理**

这一组把 `-upstream` / `-upstream-mode` / `-upstream-auth` 用上。  

需求：  
- 你在本机有一个上游 SOCKS5：`127.0.0.1:1081`，账号 `u1:p1`  
- 所有待测代理都必须通过这个上游出去（多级代理链）  

#### 示例命令

```bash
go run main.go \
  -ip ip.txt \
  -auth auth.txt \
  -mode auto \
  -allow-nonlocal \
  -upstream 127.0.0.1:1081 \
  -upstream-mode s5 \
  -upstream-auth u1:p1
```

#### 说明

- `buildUpstreamDialer` 会创建一个 SOCKS5 上游 dialer  
- 对每个待测代理的连接，都会先经过上游  
- 既支持被测代理为 HTTP，也支持 SOCKS5（取决于 `-mode` 或自动探测）  

---

### 场景 6：**上游为 HTTP 代理 + 带认证，再去测下一层 HTTP 或 SOCKS5**

这一组换一下 `-upstream-mode` (`http`)。  

假设：  
- 上游 HTTP 代理：`upstream.proxy.local:8080`  
- 上游认证：`admin:123456`  
- 你要测一批**下游 SOCKS5 代理**（多级链：HTTP → SOCKS5）  

#### 示例命令

```bash
go run main.go \
  -ip ip_socks5_targets.txt \
  -auth auth.txt \
  -mode s5 \
  -allow-nonlocal \
  -upstream upstream.proxy.local:8080 \
  -upstream-mode http \
  -upstream-auth admin:123456
```

#### 说明

- 上游走 HTTP CONNECT  
- 下游用 SOCKS5 协议  
- 链路：**你 → 上游 HTTP → 待测 SOCKS5 → 目标 IP API**  

> 也可以反过来：`-mode http` + `-upstream-mode s5`，即 SOCKS5 上游 + HTTP 下游。  

---

### 场景 7：**指定 HTTPS 代理模式**

如果你明确知道待测代理是「HTTPS 代理」：  

#### 示例命令

```bash
go run main.go \
  -ip https_proxy_list.txt \
  -auth auth.txt \
  -mode https \
  -allow-nonlocal
```

#### 说明

- `mode=https` 内部仍然通过 `testHTTPProxy`，但是：  
  - 链接上游代理时会先 TLS 握手（`useTLS=true`）  
- 输出时协议前缀为 `https://`：  

```text
https://user:pass@1.2.3.4:443#[ISP][type][Country]
```

---

### 场景 8：**不显式写大部分参数，全部走默认**

这组是「最省事」写法，实际工作也比较常用，只改你真正需要的参数。  

假设：  
- 本机只测内网代理  
- `ip.txt` / `auth.txt` 使用默认  
- 不关心输出文件名  

#### 示例命令

```bash
go run main.go
```

#### 等价于

```bash
go run main.go \
  -ip ip.txt \
  -auth auth.txt \
  -mode auto \
  -allow-nonlocal=false \
  -threads 0 \
  -delay 10ms
```

---

## 六、参数使用建议与注意事项

### 1. 关于 `-allow-nonlocal`

- 默认 = `false`，只测 **127.0.0.0/8 + 内网 + 链路本地**  
- 这是一个安全保护：避免一不小心变成大范围扫公网代理  
- 你要测公网代理时：  
  - 确认授权  
  - 手动加 `-allow-nonlocal`，相当于是显式「我知道自己在干嘛」  

> ✅ 建议：公网测试时，搭配较小的 `-threads` 与合适的 `-delay`，遵守对方服务条款。

---

### 2. 关于 `-threads` 和 `-delay`

- 默认 `-threads=0`：自动 = CPU核 × 2000，举例：  
  - 8 核 → 16000 worker（非常猛，会压满 CPU + 触发很多 IP API QPS）  
- 强烈建议：  
  - **本地测试**：可以大些，如 `-threads 2000` `-delay 5ms`  
  - **公网 + 外部 IP API**：建议从小一点起步，比如：  
    - `-threads 200 ~ 1000`  
    - `-delay 20ms ~ 100ms`  

---

### 3. 关于 `-mode`

- `s5`：只当 SOCKS5 代理探测  
- `http`：只当 HTTP 代理探测  
- `https`：当支持 TLS 的 HTTP 代理探测（走 TLS CONNECT 到上游）  
- `auto`：根据端口猜：  
  - 端口 `1080` → 先 SOCKS5 再 HTTP  
  - 端口 `80, 8080, 3128, 8000, 8888` → 先 HTTP 再 SOCKS5  
  - 其它端口 → 先 HTTP 再 SOCKS5  

> 实际使用中：  
> - 如果你清楚是 SOCKS5，用 `-mode s5` 更快  
> - 混合环境用 `-mode auto`，但会多一次失败尝试  

---

### 4. IP / Auth 文件的小技巧

- IP 文件可以带注释：以 `#` 开头的行会被跳过  
- auth 文件同样支持注释行 / 空行  
- auth 行如果不是 `user:pass` 形态，会打印警告并跳过  

---

### 5. 合法性与合规性小提醒

这个程序本质上是一个「大规模代理连通性检测工具」，**威力不小** (￣▽￣;)  

温柔但严肃的提醒一下：  

- ✅ 只测试你自己拥有或明确获得授权的代理 / 节点  
- ✅ 遵守所在国家/地区法律 & 对方服务条款  
- ✅ 避免对公开服务造成过高负载（尤其是 IP API、代理服务端）  

---
