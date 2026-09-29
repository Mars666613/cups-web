# 反向代理与 HTTPS

本文解释 cups-web 放在反向代理后面时的跨源防护与 cookie 行为，以及最典型的故障
「内网能登录，反代后提示密码错误」的成因与修法。

对应代码：`internal/middleware/security.go`、`internal/auth/session.go`。

## 快速排查表

| 现象 | 原因 | 修法 |
| --- | --- | --- |
| 登录提示「请求被拒绝（403）」 | 跨源防护判定跨源 | 见下文两类 403 |
| 登录成功但立刻回到登录页 | cookie 带了 `Secure` 而浏览器实际走 HTTP | `COOKIE_SECURE=false` |
| 提示「接口不可达（404/405）」 | 代理没把 `/api` 转发到 cups-web | 检查 `location` 与 `proxy_pass` |
| 上传大文件 413 | 代理限制了请求体 | nginx `client_max_body_size 200m;` |
| 转换 / 驱动安装中途断开 | 代理读超时 | nginx `proxy_read_timeout 300s;` |
| 页面白屏、静态资源 404 | 挂在子路径下 | 见「子路径挂载」 |

## 「登录提示密码错误」是怎么来的（issue #99）

这是最容易把人带偏的一个故障，值得完整记录。

链路上有三段：

1. **服务端**：`middleware.CrossOriginProtection()` 包的是 Go 标准库
   `http.CrossOriginProtection`。它在 `Sec-Fetch-Site` 头缺失时退化为比对
   「`Origin` 的 host 是否等于 `r.Host`」。
2. **反代**：nginx 若不写 `proxy_set_header Host $host;`，默认发给后端的是
   `$proxy_host`（形如 `localhost:1180`）。于是 `Origin`（用户输入的域名）与
   `r.Host`（代理自报的上游地址）**天然不相等** → 整站所有 POST 被 403。
3. **前端**：旧版 `LoginView.vue` 对登录失败的兜底逻辑是「响应体解析不出 JSON
   就显示『用户名或密码错误』」。而标准库的 403 是 `text/plain`，于是一个纯粹的
   代理配置问题被渲染成了凭据错误。

三段都做了修改：服务端补 `X-Forwarded-Host` 比对并把拒绝响应改成 JSON，前端按
状态码分级出真实原因。

### 两类 403，分别怎么处理

拒绝响应现在是结构化 JSON（`code: "cross_origin_blocked"`），错误文案里直接写了
原因；同时服务端会打一条含全部判定依据的日志，便于事后核对：

```
[cross-origin] 拒绝请求: method=POST path=/api/login host="localhost:1180" \
  origin="http://print.example.com" sec-fetch-site="" \
  x-forwarded-host="" x-forwarded-proto=""
```

**第一类：`sec-fetch-site=""`（头缺失）+ Origin 与 Host 不一致。**
代理没转发真实域名。补上这两行即可：

```nginx
proxy_set_header Host             $host;
proxy_set_header X-Forwarded-Host $host;
```

只要 `X-Forwarded-Host` 与 `Origin` 的主机名一致就会放行，**不需要**任何环境变量。
端口不一致也没关系：比对先按完整 host，失败再退回只比主机名——代理链里端口信息
极易丢失（nginx 的 `$host` 不含端口，而浏览器 `Origin` 在非默认端口下一定带端口），
要求端口一致会重新引入假阳性。

**第二类：`sec-fetch-site="same-site"` 或 `"cross-site"`。**
这是**浏览器自己**判定的跨源，服务端改头没有用。常见成因：

- 页面走 `http://` 而接口被升级到 `https://`（浏览器的 HTTPS-First、HSTS，或代理
  对 `/api` 做了 301 到 https）→ 协议不同即 `same-site`
- 从 `a.example.com` 的页面访问 `b.example.com` 的接口

修法是**始终用同一个协议加域名访问**。若确实需要跨源，用环境变量声明：

```yaml
environment:
  - TRUSTED_ORIGINS=https://print.example.com,http://print.example.com
```

> 值必须是完整的 origin（含协议），逗号分隔，不带路径和尾斜杠。

## 为什么默认信任 `X-Forwarded-Host` 不削弱安全

`X-Forwarded-Host` 是客户端可伪造的头，默认信任它看着可疑，但在这个具体位置上
不引入任何新的攻击通路：

- 该比对**只在 `Sec-Fetch-Site` 缺失时**生效。真实浏览器自 2023 年起都会发送该头，
  跨站请求会被标记成 `cross-site` / `same-site` 并在标准库那一层直接拒掉，走不到
  这里（`internal/middleware/security_test.go` 里有对应用例锁死这条边界）。
- 能自由伪造 `X-Forwarded-Host` 的客户端（curl、脚本）本来就能通过「既不发
  `Sec-Fetch-Site` 也不发 `Origin`」让标准库 fail-open 放行——这是标准库有意的设计
  取舍，因为跨源防护的目标是浏览器发起的 CSRF，不是脚本化请求。

也就是说这条放行只把反代下的合法请求从假阳性里救出来。真正防脚本化 CSRF 的是
`ValidateCSRF` 的 double-submit cookie（`internal/middleware/csrf.go`）。

## cookie 的 `Secure` 属性

`COOKIE_SECURE` 三态，缺省是 `auto`：

| 值 | 行为 |
| --- | --- |
| 缺省 / `auto` | 逐请求判定：直连 HTTPS 看 `r.TLS`，TLS 卸载在代理上时看 `X-Forwarded-Proto` 第一跳 |
| `true` | 恒开 |
| `false` | 恒关 |

`auto` 让 HTTP 内网部署行为完全不变，同时把 HTTPS 部署自动收紧——所以反代务必转发
`X-Forwarded-Proto $scheme`。

⚠️ 若代理错误地上报 `X-Forwarded-Proto: https` 而浏览器实际走 HTTP，浏览器会丢弃
带 `Secure` 的 cookie，表现为**登录接口返回 200 但立刻被弹回登录页**。此时用
`COOKIE_SECURE=false` 显式关闭。

## Caddy

Caddy 的 `reverse_proxy` 默认就会设置 `Host`、`X-Forwarded-Host`、
`X-Forwarded-Proto` 和 `X-Forwarded-For`，开箱即用：

```caddyfile
print.example.com {
	reverse_proxy localhost:1180
	request_body {
		max_size 200MB
	}
}
```

## Traefik

Traefik 同样默认转发 `X-Forwarded-*` 全套。注意它默认会保留原始 `Host`，无需额外
中间件。若用了 `stripPrefix` 把服务挂到子路径，见下一节。

## 子路径挂载（不支持）

把 cups-web 挂到 `https://example.com/cups/` 这样的**子路径下目前不可用**：
`frontend/vite.config.js` 没有配置 `base`，构建产物里的资源引用是绝对路径
（`/assets/...`），子路径反代会导致静态资源 404、页面白屏。

请用**独立的域名或子域**，或用独立端口。

## 登录限流与真实 IP

`cmd/server/login_limiter.go` 的 `clientIP()` 取 `X-Forwarded-For` 的第一跳作为限流
键（键为 `IP|用户名`，5 次失败锁定 15 分钟）。

⚠️ 该头被**无条件信任**，没有可信代理列表。这意味着：

- 反代部署下限流按真实客户端 IP 生效，符合预期——前提是代理设置了
  `X-Forwarded-For $proxy_add_x_forwarded_for`。
- **把 cups-web 的端口直接暴露到公网**时，攻击者每次请求换一个伪造的
  `X-Forwarded-For` 即可绕过锁定。所以公网部署应只暴露反代端口，用防火墙
  挡住后端直连。新版 compose 为 host 网络模式（issue #107），没有 Docker
  端口绑定可用——可把 `LISTEN_ADDR` 改成 `127.0.0.1:1180` 让 Web 只监听
  回环地址（cupsd 的 631 端口则用防火墙或 `cupsd.conf` 的 `Listen`/`<Location>`
  限制）。

## 相关

- [AGENTS.md 的「🔐 认证与安全」](../AGENTS.md#-认证与安全)——鉴权链与 cookie 约定速查
- [docker-build.md](docker-build.md)——网络模式与 compose 配置理由
