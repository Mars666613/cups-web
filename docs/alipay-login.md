# 支付宝授权登录

顾客扫码进系统时**没有账号**，也不该为了打印去填表单、记密码。这个功能让顾客一键
用支付宝登录，身份在建单之前就确定 —— 打印记录、余额从第一单起就是干净的。

与**支付**完全解耦：登录只拿一个身份（`open_id`），付款仍走原来的订单 + 主动查单链路，
一行没改。

## 为什么是支付宝

| 方案 | 结论 |
| --- | --- |
| 短信验证码 | 公司否了（其实 ~0.03 元/条，不贵，但不是本次方向） |
| 微信登录 | **不可行**：需企业资质 + 300 元/年认证 + **ICP 备案域名**（微信明确不接受内网 IP），且必须从微信内置浏览器打开 |
| 匿名设备账号 | 用户已否决：身份认的是"这台浏览器"而不是人，换手机/清缓存就丢，打印记录的价值大幅缩水 |
| **支付宝授权登录** | ✅ 采用。**免费**、**个人实名账号即可**、**不校验备案** |

⚠️ **「个人账号只能做沙箱 / 支付产品必须企业资质」是过时的说法，别再往文档里写。**
实测：**同一个个人实名账号**既签了「获取会员信息」（授权登录），
也签下了「**手机网站支付 + 电脑网站支付**」，付款全链路走通。
公司前期也计划先用个人账号。

> 这类「市场规则」最容易被凭印象写进文档 —— 网上抄来的、几年前的、或者只对
> 某些产品成立的。**以开放平台控制台当前的提示为准**，别以任何文档为准。
> 上线换账号的完整步骤见 [支付宝正式环境配置手册.md](支付宝正式环境配置手册.md)。

关于"必须 ICP 备案域名 + HTTPS + 80/443"的说法：**那也是第三方博客的，不对**。
实测在内网把回调地址配成了 `http://192.168.1.22:5173/#/print` ——
内网 IP + http + 非标准端口 + hash 路径**全部被接受**。

## ⚠️ iOS 必须 HTTPS —— 这是上线前的硬前提

**支付宝 iOS 客户端是基于 WKWebView 的，iOS 的 ATS 默认禁止加载 http 明文**
（App 必须在 `Info.plist` 里显式放开，支付宝没有为普通网页放开这个口子）。

| 设备 | 支付宝内置浏览器加载 `http://192.168.1.22:5173` |
| --- | --- |
| 安卓 / 鸿蒙 | ✅ 正常 |
| **iOS** | ❌ **拦截** → 白屏 |

iOS 上的表现是两段：先弹「是否前往外部网站」（支付宝跳离自有页面时的安全确认），
用户点确认后**一直空白加载**。而提示里的网址正是我们的回调地址 —— 说明授权其实已经
做完，卡在最后一步把 `auth_code` 交回来。

🚫 **改代码救不了这个**，这是渠道客户端的限制。**上线的公网地址必须是 `https://`**，
否则 iOS 顾客一路白屏。已与用户确认生产就是 https。

> 开发期内网 http 验证不了 iOS，可临时用内网穿透（ngrok / cpolar）拿一个
> `https://` 地址在 iPhone 上跑一遍 —— 这一步同时能回答"https 是不是唯一原因"。

### 已知的次要风险：回调地址里的 `#`

支付宝开发者社区有反馈：**授权链接里带 `#` 在 iOS 端会造成 bug**。当前的
`…/#/print` 是带 `#` 的。目前无法判断它有没有起作用（http 那关先拦住了，看不到后面）。

若生产 HTTPS 下 iOS 仍失败，**第一件事就是把回调换成无 `#` 的形式**：

```
https://<域名>/alipay-callback
```

后端不用改 —— SPA fallback 会把 `index.html` 吐出来，而前端 `readAlipayAuthReturn()`
本来就同时读 hash 内和 hash 外两种落点（支付回跳踩出来的经验：支付宝会丢 fragment）。
**只需改支付宝后台那一处配置 + `paymentReturnURL` 的调用点**。

## 流程：授权在支付宝里做，会话交回浏览器

第一版是"全程在支付宝 webview 里跑完"，真机验证后推翻了 —— **支付宝的 webview 干不了活**：

1. 它的文件选择器翻不到**微信/QQ 里发来的文件**（浏览器用的是系统文件管理器，有「快捷访问」）
2. `pdf.js` 的 worker 在里面起不来，**PDF 预览一直转圈**（浏览器里正常）

而打印场景要的文件，一大半正是微信/QQ 发过来的。所以改成
「**授权在支付宝里做，会话交回浏览器**」—— 微信 PC 扫码登录是同一套模式：

```
浏览器   /start（建一行状态）→ alipays:// 把授权页交给支付宝 → 开始轮询
支付宝   静默授权 → 回跳 …/#/print?auth_code&state → /exchange（把用户写进那行状态）
浏览器   /poll 轮询到 → 给自己签发会话 → 继续上传/支付
```

### 为什么`state` 必须落在服务端

**浏览器与支付宝 webview 是两个互不相干的 cookie 罐。** 把 `state` 放签名 cookie 里，
webview 那次回跳根本读不到 —— 所以它在 `alipay_login_states` 表里。

| 路由 | 说明 |
| --- | --- |
| `GET /api/auth/alipay/start` | 建状态行，返回 `{authorizeUrl, state, fromBrowser}`（**返回 JSON 不是 302**：端外唤起得由前端发 `alipays://`，后端套不了自定义 scheme，前端还要留唤起失败的兜底） |
| `POST /api/auth/alipay/exchange` | 验状态行 → 换 `open_id` → 找/建用户；**`fromBrowser=false` 时才签发会话**，否则返回 `needBrowserReturn` |
| `GET /api/auth/alipay/poll?state=` | 浏览器轮询。**用 200 + `status`，不用 4xx** —— 4xx 会被全局兜底当成会话失效弹登出 |

- 三条都挂在 `main.go` 的 **public 段**（与 `/login` 同级）：顾客此刻还没有会话，这正是登录的目的。
- 支付宝扫一扫直接进来的**没有轮询方**，靠 `fromBrowser` 区分两种收尾。
- 状态行 15 分钟过期（`alipayLoginStateTTL`），`/start` 里顺带惰性清理。

🚫 **支付宝授权后不会自动回到原浏览器**，这是设计限制。`exitApp` / `popWindow` 两个
JSBridge 方法在真机上都试过，全部落在支付宝首页（安卓返回栈里我们的 webview 位于其首页之上）。
所以现在给的是一个**明确的死胡同提示页**（「授权完成，请按返回键回到浏览器」），
**不自动关页** —— 自动关页会让用户一脸茫然地站在支付宝首页，连"发生了什么"都不知道。

## 接口事实

### `alipay.system.oauth.token`

- 业务参数 `grant_type`（`authorization_code` / `refresh_token`）、`code`
- ⚠️ **`grant_type` / `code` 是【顶层表单字段】，不能套 `biz_content`**。
  套进去网关返回 `isv.grant-type-invalid`（40002 Invalid Arguments），报错完全不指向根因。
  代码里为此分了 `call()`（套 `biz_content`）与 `callFlat()`（直接作顶级表单项）两条路。
- 响应节点 **`alipay_system_oauth_token_response`**（在顶层），含
  `access_token` / `expires_in` / `refresh_token` / `re_expires_in` / **`open_id`** / `auth_start`
- ⚠️ **这个接口的成功响应里【没有 `code` 字段】**。用 `resp.Code != "10000"` 判失败会把
  **每一次成功都当成失败**，而且错误信息是空串。必须写成
  `resp.Code != "" && resp.Code != "10000"`。
  （踩过：界面报「支付宝授权失败: 」后面什么都没有。）

### `open_id` 而不是 `user_id`

官方原文：「新商户建议使用 **open_id** 替代 user_id……user_id 字段未来计划逐步回收。
如使用 open_id，请确认 应用-开发配置-**openid 配置管理**已启用」。

→ 我们用 `open_id`（128 字符）。**它会逼出超长的用户名**，见下。

### 授权链接不需要签名

```
https://openauth.alipay.com/oauth2/publicAppAuthorize.htm
    ?app_id=<APPID>&scope=auth_base&redirect_uri=<urlencoded>&state=<...>
```

- ⚠️ **授权主机与网关不是同一台**：授权是 `openauth.alipay.com`，网关是 `openapi.alipay.com`。
  沙箱对应 `openauth.alipaydev.com` / `openapi-sandbox.dl.alipaydev.com`。
  代码按 `ALIPAY_GATEWAY` 是否含 `sandbox`/`alipaydev` 自动推断，`ALIPAY_AUTH_URL` 可显式覆盖。
- `scope=auth_base` → **静默授权**，登录后直接跳回、**无确认页**（符合"点一下就登上了"）。
  要昵称头像才需要 `auth_user`，本场景不需要。
- `auth_code` **一次有效**，未使用时 3 分钟~24 小时动态过期 → 兑换失败要提示
  「授权已过期，请重新点击支付宝登录」，不是报错卡住。

### 手机网站登录只能在支付宝客户端内

官方原文：端外「拼接的授权链接**不支持客户端端外打开**……会提示：**请在支付宝客户端打开链接**」。
官方给的端外唤起方式是 `alipays://platformapi/startapp?appId=20000067&url=<encodeURIComponent(授权链接)>`。

系统浏览器里直接开授权链接是**死路**（那句提示里没有任何可点的按钮），必须先唤起 App。

## 用户名：`alipay:` + 6 位随机字符

`users.username` 是 `NOT NULL UNIQUE`，`open_id` 有 128 字符、直接铺出来认不出谁是谁。

| 字段 | 取值 | 理由 |
| --- | --- | --- |
| `username` | `alipay:` + 6 位 base32 随机 | 短到能显示、能念给管理员听。**不是** `alipay:<open_id>` |
| `alipay_open_id` | 128 字符原值 | 真正的身份凭据，只在这里 |
| `password_hash` | `bcrypt(随机 token)` | 照访客账号的既有做法 —— 存**有效哈希而非空串**，更难被将来的代码误判 |
| `role` | `user` | `RequireAdmin` 只看这个字段 |

- 用户名**唯一性靠重试**（5 次），撞了就换一个随机后缀；`open_id` 的唯一性靠
  `idx_users_alipay_open_id` 这条**部分唯一索引**。
- 🚫 **`alipay_open_id` 不进 `UpdateUserInput`** —— 它是身份凭据，不该被前台改。
- 🚫 **管理员不能把用户改名成 `alipay:` 前缀**（`errAlipayNamespace`）—— 那是系统命名空间，
  放进来会与自动建号撞名。
- 🚫 **登录时不接受 `alipay:` 前缀的密码登录**。

⚠️ **个人中心必须显示【原始用户名】并给一个复制按钮。** 曾经把它美化显示成「支付宝用户」，
结果是顾客要退款、联系管理员时报不出自己是谁 —— 管理员根本查不到该退哪一单。
**顾客唯一的身份凭据就是这个字符串，不能替他藏起来。**

管理端用户列表则相反：`userLabel()` 截断显示（≤24 字符）+ `title` 上留完整值，
并打一个「支付宝」角标。列表页支持按用户名**子串**查询（`?username=`，用 `position()`
而不是 `LIKE` —— 搜索框里打一个 `%` 不能变成"查出所有人"）。

## 文件来源：微信/QQ 的文件搬不过来

微信/QQ 的文件在各自沙箱里，**系统文件选择器看不到**；且微信的文件只有微信生态拿得到、
支付宝相关的能力只有支付宝能用，**两边互斥**。

所以"微信发来的文件 + 支付宝收款"中间那一步只能顾客自己搬（先**存到手机**再选）。
登录页有对应的图文指引 —— 这是产品取舍，不是缺陷。想消掉它 = 微信原生化
（小程序 + 微信支付 + 备案），另一个量级的投入。

## 配置

全走**环境变量**（与支付同一套，见 `.env.example`）：

| 变量 | 说明 |
| --- | --- |
| `ALIPAY_APP_ID` / `ALIPAY_PRIVATE_KEY` / `ALIPAY_PUBLIC_KEY` | 与支付共用 |
| `ALIPAY_GATEWAY` | 与支付共用 |
| `ALIPAY_AUTH_URL` | **可选**，授权页地址。留空按 Gateway 是否沙箱自动推断，只有非常规部署才需要显式指定 |

支付宝后台要配的**只有一处**：应用-开发配置-**授权回调地址**，填
`<站点地址>/#/print`（生产即 `https://<域名>/#/print`）。**一个应用只能配一个地址** ——
但所有门店访问的都是同一个公网地址，所以不构成问题。

> ⚠️ `ALIPAY_PRIVATE_KEY` **绝不能写进日志**。启动日志只打 `app_id` 与网关。

## 相关文件

| 文件 | 内容 |
| --- | --- |
| `cmd/server/alipay_auth.go` | 三个 handler + 惰性建号 + UA 判定 |
| `cmd/server/payment.go` | `buildAlipayClient()` / `alipayFromEnv()` |
| `internal/payment/alipay_oauth.go` | `AuthorizeURL()` / `ExchangeAuthCode()` |
| `internal/store/alipay_states.go` | `alipay_login_states` 表的读写 |
| `internal/store/users.go` | `AlipayOpenID` 字段 + `GetUserByAlipayOpenID` + `UserFilter` |
| `frontend/src/utils/alipayAuth.js` | `beginAlipayLogin()` / `pollUntilSettled()` |
| `frontend/src/App.vue` | `handleAlipayAuthReturn()`（setup 阶段同步读 URL，守卫会冲掉 query） |
