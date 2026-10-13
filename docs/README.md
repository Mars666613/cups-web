# 深度文档索引

本目录收录 [AGENTS.md](../AGENTS.md) 中各章节的原理说明、故障案例与历史决策。AGENTS.md 本身保留可快速扫读的规则手册（API 表、DB 表、目录约定、退出码等），需要理解"为什么"时来此处查阅。

| 文件 | 内容 |
| --- | --- |
| [architecture.md](architecture.md) | 技术栈选型、外部依赖坑位（LibreOffice 可写 HOME、Ghostscript 字体破坏性改造、dpkg-dev 缺失） |
| [pdf-pipeline.md](pdf-pipeline.md) | PDF 标准化三链路、Ghostscript cidfmap 两套加载机制、空壳 CJK 字体与 pdf.js 预览错位、HTTP 超时设计 |
| [driver-management.md](driver-management.md) | 驱动持久化原理、manifest 白名单翻车案例、AIO 单一 EXIT trap、退出码由来、架构探测、`.deb` 上传机制、`lpinfo` 解析、异步任务模型 |
| [container-startup.md](container-startup.md) | entrypoint 10 步流程、cupsd watchdog 子 shell 127 死循环、fast-fail 退避、restore-drivers 永远 exit 0 |
| [docker-build.md](docker-build.md) | 五阶段构建设计、三架构基础镜像选型史、ca-certificates 勿删、HOME/LibreOffice profile、docker-compose 配置理由、CI/CD |
| [reverse-proxy.md](reverse-proxy.md) | 反代下「登录提示密码错误」的三段成因、两类跨源 403 的分别修法、为什么默认信任 `X-Forwarded-Host`、cookie `Secure` 三态、Caddy/Traefik、子路径限制、限流与真实 IP |
| [刷卡取件终端部署手册.md](刷卡取件终端部署手册.md) | **正式环境的部署手册**（给去现场装机器的人，照着从第一条做到最后一条）：买读卡器前必须验的卡型、从本机 scp 到终端机器、装依赖、写配置、自检、开机自启、**绑卡的三种方式**、五步验收、声码表、排障 |
| [pull-printing.md](pull-printing.md) | **刷卡取件（企业内网）**：与计费/访客三方互斥、复用订单机制存 `held`、**`held` 必须在 `ListActiveOrderFiles` 的保护名单里**（漏了会静默删文件）、释放失败退回 `held` 而非退款、孤儿恢复的判据、卡号为何必须刷卡绑定、无屏幕终端的声码与树莓派部署 |
| [支付宝正式环境配置手册.md](支付宝正式环境配置手册.md) | **上线用的支付宝配置手册**：这个系统用到哪 4 个能力、要签哪 3 个产品、密钥怎么生成与上传、**支付宝公钥 ≠ 应用公钥**、回调地址、环境变量与启动日志自检、上线前检查清单、真机验证、**从个人账号换成公司账号的分步流程**、排障 |
| [alipay-login.md](alipay-login.md) | 支付宝授权登录：**iOS 必须 HTTPS**（WKWebView 拦 http 明文）、`state` 为何必须落库、`grant_type` 只能作顶级表单项、成功响应没有 `code` 字段、用户名为何是 `alipay:` + 6 位随机、微信/QQ 文件搬不过来的取舍 |
| [conventions.md](conventions.md) | 常见开发任务步骤模板（新增 API/DB/页面/文件类型/驱动）、调试测试、Go/Vue 代码风格、Git 提交约定 |
| [wechat-pay.md](wechat-pay.md) | **微信支付接入前的待决问题**（尚未实现）：产品选型（Native/H5/JSAPI）、备案与资质门槛、内网下 H5 为何不可行、接口适配问题、为什么先不写签名（42 个全绿全错的用例）、绊线测试 |
