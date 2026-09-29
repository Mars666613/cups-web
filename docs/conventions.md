# 开发约定与常见任务

> 本文档是 [AGENTS.md](../AGENTS.md) 的补充，收录常见开发任务的步骤模板、调试方法与代码风格约定。

## 常见开发任务

### 新增 API 接口

1. 在 `cmd/server/` 下新建 `xxx_handler.go`，导出 handler 函数
2. 在 `main.go` 对应的 subrouter（`api` / `protected` / `admin`）中注册路由
3. 前端在 `frontend/src/utils/api.js` 中新增调用方法，并在视图中使用
4. 若是写接口，确认前端 `fetch` 会带上 `X-CSRF-Token` 头

### 修改数据库结构

1. 在 `internal/store/` 中修改或新增模型
2. 在 `store.go::migrate()` 中：
   - 新表：追加 `CREATE TABLE IF NOT EXISTS ...`
   - 旧表加字段：用 `addColumnIfMissing(ctx, db, "<table>", "<column_def>")`
3. 更新对应的 CRUD 函数
4. 本地用 `sqlite3 data/cups-web.db` 验证迁移在新库与老库上都能跑通

### 新增前端页面

1. 在 `frontend/src/views/` 新建 `.vue`，使用 Composition API
2. 在 `frontend/src/router/index.js` 添加路由；若需鉴权用 `meta: { requiresAuth: true }`，管理员页加 `requiresAdmin: true`
3. 在 `App.vue` 顶栏中按需加入导航入口（当前实现对 `admin` 角色显示「打印 / 驱动 / 管理」三个入口，桌面端是分段切换、移动端进汉堡菜单）

### 新增支持的文件类型

1. 在 `file_utils.go::detectFileKind` 加入新的 `fileKind`
2. 实现转换函数（放 `convert_utils.go` 或 `pdf_utils.go`）
3. 在 `print_handlers.go` 的 `switch kind` 中处理新类型
4. 同步更新 `estimateHandler` / `convertHandler` 中的分支（`convertHandler` 需覆盖单文件 `file` 与多文件 `files` 两种入口）

### 新增支持的打印机驱动

细节与踩坑理由见 [driver-management.md](driver-management.md)，步骤如下：

1. **写安装脚本**：`scripts/driver/install-<name>.sh`。文件名里的 `<name>` 就是驱动的 canonical name，`Dockerfile` 的 `COPY scripts/driver/install-*.sh /opt/cups-drivers/scripts/` 会自动带上，无需改 Dockerfile。
2. **遵守退出码约定**：`0` = 成功；**`3` = 当前架构不支持**（绝不能用 `exit 0` 糊过去，否则会写出 manifest、Web UI 假显示"已安装"）；其他非零 = 真失败。架构判断一律用 `dpkg --print-architecture`（**不要用 `dpkg-architecture`**，runtime 镜像没有 `dpkg-dev`）。
3. **遵守单一 EXIT trap 约定**（只有需要现场编译 / 装编译依赖时才涉及）：整个脚本**只允许一个** `trap _cleanup EXIT`，临时目录清理和 `apt-get purge -y --auto-remove ${BUILD_DEPS}` 都写进 `_cleanup()` 的分支里；用 `CUPS_AIO` 环境变量（`driver-install` 会 `export CUPS_AIO=1`，Go 侧 `runDriverCommand` 也在 `cmd.Env` 里加了）区分"运行时容器内安装"与"构建期安装"。**AIO 模式下不要 `rm -rf /var/lib/apt/lists/*`**，否则装下一个驱动就没有 apt 索引了。
4. **选对持久化通道**：
   - **deb 来源的驱动**（厂商 `.deb`、或 `apt-get install`）走**包级通道**：在下载成功之后、`dpkg -i` 之前加一行把 `.deb` 原件交接给 `driver-install` 归档 ——
     ```bash
     # 故意 `|| true`：归档失败不影响安装成败判定，也不改变退出码语义（0/3/其他）；
     # 变量未设置时（构建期或手工执行）行为与以前完全一致。绝不新增 trap。
     if [ -n "${DRIVER_PKG_DIR:-}" ]; then
         cp -a "${DEB_PATH}" "${DRIVER_PKG_DIR}/" 2>/dev/null || true
     fi
     ```
     `apt-get install` 来源的不用手动交接（apt 钩子 `capture-debs` 会自动抓，含全部传递依赖），但**要避免让 apt 去满足 `cups` 元包依赖** —— 用 `dpkg -i --force-depends` 而不是 `apt-get -f install`，否则会装上 Debian 的 `cups-core-drivers` 覆盖源码编译的 CUPS 组件。
   - **非 deb 来源**（源码编译、手工 cp、unzip）走**文件级通道**：产物必须落在白名单目录内（`/usr/lib/cups`、`/usr/share/cups`、`/usr/share/ppd`、`/usr/share/foomatic`、`/lib/firmware`、`/usr/lib/firmware`、`/usr/lib/<multiarch>`）。⚠️ 用 autoconf 时注意 `--libdir` 默认是**裸 `/usr/lib`**（不在白名单内），共享库要显式装到 multiarch 目录。
   - 文件级列表与包级归档**同时**为空时 `driver-install` 才判失败。
5. **注册到 `driver_registry.go::driversRegistry`**：填 `Name`（= 脚本名里的 `<name>`）、`DisplayName`、`Description`、`Arch`（`{"all"}` 或 Debian 架构名列表，决定前端「安装」按钮是否可点）、`NeedCompile`（是否现场编译，前端据此提示耗时）、`MatchPatterns`（`(?i)` 正则，供 `/drivers/detect` 按型号推荐；纯通用驱动可留空）。
6. **下载源**：第三方驱动一律走本仓库自维护的 GitHub Releases 镜像（tag 固定为 `cups-driver`），不要直连厂商 CDN（Epson/Sharp 的官方下载站有 UA/TLS 指纹风控，CI 里 403 概率高）。失败要 fail-fast（非零退出），不要静默成功。
7. **验证**：容器内 `driver-list` 看是否出现在可用列表（会显示 `Restore: package/files/hybrid`）、`driver-install <name>` 跑通、`cat /opt/cups-drivers/data/<name>/manifest.txt` 检查清单里**没有**系统文件（尤其没有 CUPS 自己的 backend/filter）、`driver-remove <name>` 后系统仍然完好（`lpstat -r` 正常、`/usr/lib/cups/backend/*` 还在）。
8. **恢复验证必须销毁重建容器**：
   ```bash
   docker rm -f <ct> && docker run -d --name <ct> -v "$PWD/.drivers:/opt/cups-drivers/data" <image>
   ```
   🚫 **不能用 `docker restart`** —— 它保留容器可写层，驱动文件本来就还在，测不出任何东西，会假通过。重建后逐项确认关键产物（filter、共享库、PPD、厂商数据目录）都回来了，`ldd` 无 `not found`。

## 调试与测试

### 后端测试

```bash
go test ./...                # 全部测试
go test -cover ./...         # 带覆盖率
go vet ./...                 # 静态检查
```

> 当前仓库主要以手工测试 + 日志为主，`test/` 目录下存放临时测试用例，不参与 CI。新增核心模块时建议补 `_test.go`。

### 前端验证

```bash
cd frontend
bun run build                # 构建检查（类型与语法）
bun run dev                  # 本地调试
```

### 数据库查看

```bash
sqlite3 data/cups-web.db
.tables
SELECT * FROM users;
SELECT id, filename, status, is_duplex, is_color, created_at FROM print_jobs ORDER BY id DESC LIMIT 20;
SELECT * FROM settings;
```

## 代码风格

### Go 风格

- 遵循标准 Go 命名约定与 `gofmt`
- Handler 内部通过 `appStore.WithTx(ctx, readOnly, func(tx) error { ... })` 做事务边界
- 错误响应统一使用 `writeJSONError(w, status, msg)`，成功使用 `writeJSON(w, v)`
- 文件路径：存储到 DB 的是 `filepath.ToSlash` 后的相对路径，使用时再用 `filepath.FromSlash` + `filepath.Join(uploadDir, ...)` 还原

### Vue 风格

- 单文件组件（SFC）+ `<script setup>` Composition API
- UI 组件优先用 `@nuxt/ui`（全局前缀 `U`，见 `vite.config.js`）
- 样式使用 Tailwind utility class，深色/浅色主题跟随 Nuxt UI 的 `bg-default` / `text-muted` 等语义类
- Session 信息通过 `router/index.js` 中的 `cachedSession` 缓存，避免每次路由切换都打 `/api/session`

### Git 提交

- Commit message 使用中文，格式 `feat:` / `fix:` / `refactor:` 等前缀 + 简要描述
- **禁止**在 commit message 中添加 `Co-Authored-By` 或任何 AI 署名行

## 相关资源

- [CUPS 官方文档](https://www.cups.org/documentation.html)
- [IPP 规范](https://www.pwg.org/ipp/)
- [Nuxt UI v4](https://ui.nuxt.com/)
- [Tailwind CSS v4](https://tailwindcss.com/)
- [Vue 3 文档](https://vuejs.org/)
- [ofdrw](https://github.com/ofdrw/ofdrw)
