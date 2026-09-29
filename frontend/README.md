# Frontend

CUPS Web 前端，基于 Vue 3 + Vite + [Nuxt UI v4](https://ui.nuxt.com/) + Tailwind CSS v4，推荐使用 [Bun](https://bun.sh/) 管理依赖。

构建产物（`dist/`）会被根目录的 `frontend/embed.go` 通过 `go:embed` 打包进 Go 二进制，因此 **发布前必须先构建前端**。

## 开发

```bash
cd frontend
bun install
bun run dev          # Vite 开发服务器（默认 :5173，/api 代理到 :8090）
```

配合后端本地调试：

```bash
# 另开终端，在仓库根目录启动后端在 :8090
LISTEN_ADDR=:8090 go run ./cmd/server
```

## 构建

```bash
bun run build        # 产物输出到 frontend/dist
```

也可以直接在仓库根目录执行 `make frontend`。

## 目录结构

```text
src/
├── main.js              # Vue app 入口
├── App.vue              # 顶层布局 + 鉴权跳转
├── router/index.js      # hash 路由 + session 守卫
├── views/               # LoginView / PrintView / AdminView
├── components/          # 业务组件
├── utils/               # api / file / format
└── index.css            # 全局样式
```

更详细的架构和 API 约定见仓库根目录的 [AGENTS.md](../AGENTS.md)。
