---
name: dev-flow
description: cups-web 的分阶段开发流程助手。用于处理需求、Bug 和优化：收集信息、分析根因、设计方案、实施修改、验证、自审、生成提交信息，并在每个阶段等待用户确认；只有得到明确确认后才提交或推送。
---

# cups-web Dev Flow

按阶段推进 cups-web 的开发任务。维护当前阶段和待确认事项；用户确认后才进入下一阶段。用户提出修正时，留在当前阶段重新整理，不要自行跳过确认点。

## 触发与范围

- 用户明确说"使用 dev-flow"、`$dev-flow`，或请求按项目开发流程处理需求、Bug、优化时使用本 skill。
- 输入包含 Issue 引用（如 `#123` 或完整 URL）时，从 Issue 开始；否则按自由描述收集信息。
- 遵守项目根目录的 `AGENTS.md`（`@AGENTS.md`）和 `CLAUDE.md`。本项目为 cups-web 单仓库，无子模块。

## 不可跳过的安全规则

- 开始修改前运行 `git status --short`，识别并保留用户已有改动；禁止 reset、checkout 或覆盖无关改动。
- 只在已确认的 scope 内读写文件。发现需要扩大 scope 时，先说明原因并重新确认。
- 读操作可以自动执行；提交、推送等不可逆操作必须分别获得明确确认。
- 不能验证时如实报告原因和剩余风险，不要用"应该可以"代替验证。

## 项目速查

| 项 | 值 |
| --- | --- |
| 后端 | Go 1.26，`gorilla/mux`，`modernc.org/sqlite`（纯 Go，无 CGO），`goipp` |
| 前端 | Vue 3.5 + Vite 7，`@nuxt/ui` v4 + Tailwind CSS v4，hash 路由 |
| 数据库 | SQLite，WAL + foreign_keys，迁移在 `internal/store/store.go::migrate()` |
| 构建 | `make all`（前端 dist → Go 二进制），Docker 五阶段构建 |
| 入口 | `cmd/server/main.go`，路由注册在同一文件 |
| 部署 | AIO 容器（cupsd + cups-web 同容器），`docker-compose.yml`，host 网络 |
| 版本 | `VERSION` 环境变量 → ldflags `-X main.Version`，回退 `git describe` |

## 阶段一：收集信息

### 有 Issue 引用

1. 从引用提取 `owner`、`repo` 和 `number`（本项目为单仓库，owner/repo 按实际 remote 判定）。
2. 获取 Issue 正文、评论、标签和状态：

   ```bash
   gh issue view <number> --repo <owner>/<repo> \
     --json title,body,comments,labels,state,url
   ```

3. 判断类型：Bug、功能需求或优化。

### 自由描述

先查 `AGENTS.md`、`docs/` 目录和相关代码建立上下文，再只询问缺失且会影响实现的关键信息。

- Bug：复现步骤、平台/版本、期望与实际行为、日志或截图、首次出现版本。
- 需求：使用场景、期望行为、涉及端（前端/后端/容器/Docker）、边界条件、兼容性或性能约束。
- 描述含糊时，先指出已知事实和未知项；不要在需求尚未清楚时开始改代码。

输出一份简短的结构化摘要：

```text
类型：Bug / 功能 / 优化
问题或目标：
复现步骤或典型用例：
影响范围（前端/后端/数据库/容器/Dockerfile/entrypoint）：
已知约束：
待确认事项：
```

然后暂停并询问：信息是否准确、完整，是否进入方案设计。

## 阶段二：分析与方案设计

### Bug

- 根据描述、日志和代码调用链定位根因；区分已证实事实、推断和待验证假设。
- 检查 `AGENTS.md` 中的踩坑记录（各类 🚫/⚠️ 标记）和 `docs/` 下的深度文档。
- 说明为何现有行为会发生、修复点、回归风险和验证方法。

### 功能或优化

输出可执行方案，至少包含：

- 期望行为和不在 scope 内的内容
- 涉及的文件：后端（`cmd/server/*.go`、`internal/*`）、前端（`frontend/src/`）、数据库（`internal/store/`）、容器（`Dockerfile`、`entrypoint.sh`、`scripts/`）
- API 路由和 handler 设计（如新增接口）
- 数据库迁移（幂等 SQL + `addColumnIfMissing`，见 AGENTS.md 表结构）
- 前端状态、路由、组件和 UI 交互
- 测试策略、兼容性、性能和安全（CSRF、会话、跨源防护、登录限流）
- 需要用户决定的选项

优先复用现有架构和接口。数据库改动遵守 `store.go::migrate()` 的幂等增量模式；CSRF/Session 遵守 `auth/` 包的统一签发方法，禁止手搓 `http.Cookie`。

展示方案后暂停，等待用户确认 scope 和实现方案。未确认时只进行只读调查和方案讨论。

## 阶段三：实施与验证

收到方案确认后：

1. 再次检查 `git status --short`，记录本次改动前的工作树状态。
2. 按确认的文件和模块实施，保持改动最小；不要顺手重构无关代码。
3. 按需执行格式化与生成：
   - Go：执行 `gofmt -w .`；必要时运行 `go vet ./...`。
   - 前端：在 `frontend/` 执行 `bun run build`（或 `bunx vite build`）验证构建通过。
   - 修改前端依赖：检查 `package.json` 和 `bun.lock`/`package-lock.json` 一致性。
4. 根据变更范围验证：
   - 后端改动：运行 `go build -o /dev/null ./cmd/server` 验证编译；有测试时运行 `go test ./...`。
   - 前端改动：运行 `cd frontend && bunx vite build` 验证构建。
   - 全栈改动：运行 `make all` 验证端到端构建。
   - 修改 Dockerfile / entrypoint.sh：检查 `docker-compose.yml` 是否受影响。
5. 检查 `git diff --check`，检查本次改动文件是否出现 UTF-8 替换字符 ``。

如果命令失败，先判断是代码失败、环境缺失还是权限问题；能修复就修复并重跑，不能修复就保留失败输出和影响范围。

输出：改动文件清单、生成物、验证命令及结果、未验证项目和剩余风险。然后暂停，等待用户确认改动结果。

## 阶段四：提交前自审

用户确认实施结果后，逐文件检查 `git diff`，必要时搜索所有调用点。至少覆盖：

| 检查项 | 关注点 |
| --- | --- |
| 逻辑正确性 | 空值、零值、负值、错误分支、边界条件 |
| API 兼容 | 请求/响应格式、CSRF 头、Cookie 属性（Path/SameSite/Secure/MaxAge）、跨源防护 |
| 数据库 | 迁移幂等性、WAL 模式、`foreign_keys`、`ErrNotFound` 语义 |
| 前端副作用 | 渲染、hash 路由、事件绑定、Vite 构建分包（vue-vendor/ui-vendor/pdf-vendor） |
| 打印流水 | 类型识别→转换→页数→IPP 提交链完整性；自定义百分比缩放 gs 参数顺序 |
| 容器与部署 | Dockerfile 阶段、entrypoint.sh 启动顺序、卷挂载路径、驱动持久化 |
| 性能与资源 | 高频路径、I/O、goroutine 生命周期、SQLite 连接 |
| 死代码与类型 | 未使用变量、不可达分支、字符串/数字比较、空值处理 |
| 回归与测试 | 相关旧功能和错误路径是否受影响 |

发现问题时立即修复并重新执行受影响的格式化和验证。输出审查结果：

```text
代码审查结果
- 逻辑正确性：通过 / 已修复问题 / 未通过
- API 兼容：通过 / 已修复问题 / 未通过
- 数据库：通过 / 已修复问题 / 未通过
- 前端副作用：通过 / 已修复问题 / 未通过
- 打印流水：通过 / 已修复问题 / 未通过
- 容器与部署：通过 / 已修复问题 / 未通过
- 性能与资源：通过 / 已修复问题 / 未通过
- 死代码与类型：通过 / 已修复问题 / 未通过
- 回归与测试：通过 / 有剩余风险
结论：无新增问题 / 已修复 <数量> 个问题 / 阻塞原因
```

审查未通过或仍有未解释的高风险时，不进入提交阶段。否则暂停，等待用户确认审查通过。

## 阶段五：生成提交信息

根据最终 diff 生成一条 Conventional Commits 信息：

```text
<type>(<scope>): <中文描述>

<用中文说明为什么改，必要时说明重要取舍>

Fixes #<number>    # Bug 完整修复
Closes #<number>   # 功能完成
Ref #<number>      # 部分完成或仅相关
```

规则：

- `type` 使用 `feat`、`fix`、`refactor`、`docs`、`chore`、`perf` 或 `test`。
- `scope` 使用实际模块，如 `print`、`driver`、`auth`、`admin`、`docker`、`frontend`、`api`、`store`、`entrypoint`。
- 提交说明和正文使用中文；🚫 禁止 `Co-Authored-By` 及任何 AI 署名行。
- 当前仓库 issue 写 `#123`。

展示提交信息后暂停，等待用户确认或修改。

## 阶段六：提交与推送

收到提交信息确认后：

1. 只 stage 本次确认的文件：

   ```bash
   git add <confirmed-files>
   git diff --cached --check
   git diff --cached --stat
   ```

2. 展示 staged 文件和摘要；若出现无关文件，取消其 staging 并重新确认。
3. 仅在用户确认提交后执行：

   ```bash
   git commit -m '<confirmed-message>'
   ```

4. 报告 commit SHA 和结果，单独询问是否推送。只有得到明确确认才执行：

   ```bash
   git push origin main
   ```

## 阶段控制

- 用户说"继续""确认""通过"时，只推进到下一个尚未确认的阶段。
- 用户提出新信息或要求修改时，更新当前阶段的摘要/方案，不要丢弃已完成的证据。
- 用户拒绝方案时，回到阶段二；用户拒绝提交时，保留代码和审查结果，不执行 commit。
- 任何阶段遇到阻塞，报告：已完成的调查、具体阻塞、尝试过的命令和需要用户提供的最小信息。