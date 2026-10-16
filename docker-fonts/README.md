# docker-fonts —— 自定义中文字体目录

本目录存放 **Docker 构建时可选安装**的中文 TrueType 字体，用于让
LibreOffice / Ghostscript 渲染出更接近原稿的字形。

## 🚫 本仓库【不包含】任何字体文件

这里曾经放着 `simsun.ttf` / `simhei.ttf` / `simkai.ttf` / `simfang.ttf`，
**已经删除**。原因：

> **SimSun / SimHei / SimKai / SimFang 是微软（中易）的版权字体，
> 只允许在你自己持有授权的 Windows 环境里使用，不得随仓库或镜像公开分发。**

把它们提交进公开仓库 = 公开分发版权字体。这不是洁癖问题，是会真的引来
法律麻烦的那一类，所以它们必须离开仓库。

## 不放字体也完全能用

镜像里**本来就装了三个开源中文字体**（见 Dockerfile 的 apt 列表）：

| 包 | 字体 | 风格 | 顶替谁 |
| --- | --- | --- | --- |
| `fonts-arphic-uming` | AR PL UMing | 明体 / 宋体一路 | 宋体、仿宋 |
| `fonts-arphic-ukai` | AR PL UKai | 楷体 | 楷体 |
| `fonts-wqy-zenhei` | 文泉驿正黑 | 黑体 | 黑体 |
| `fonts-noto-cjk` | Noto Sans/Serif CJK | 无衬线 / 衬线 | 通用兜底 |

`cidfmap.local` 与 `fontconfig-chinese.conf` 默认就指向它们。差别只在字形：
开元字体的字形与 Windows 那几套不完全一样（尤其中文表格、准考证这类
排版讲究的文档看得出来），但**内容不会错、不会变方块**。

## 想要更接近原稿的字形？自己放

如果你（或你的客户）**合法持有**这些 Windows 字体，可以把
`simsun.ttf` / `simhei.ttf` / `simkai.ttf` / `simfang.ttf`
放进**本目录**，然后正常构建：

```bash
docker build -t cups-web:latest .
```

构建时会自动：

1. 把它们装进镜像的 `/usr/share/fonts/truetype/custom/`
2. **自动改写** `cidfmap.local` 里的路径，把开源字体换成你放的这几个
   （见 Dockerfile 里那几条条件 `sed`）

**你不需要改这个仓库里的任何文件** —— 放了就生效，不放就走开源字体。

> ⚠️ 这样构建出来的镜像**你自己用没问题**，但**不要公开分发这个镜像**，
> 也不要把字体提交回仓库 —— 那等于你在替传播版权字体。要公开分发就用
> 默认（不含字体的）构建。

## 关于格式

**只接受 `.ttf` / `.ttc`**，不要放 `.otf`。

Ghostscript 的 cidfmap 用 `/FileType /TrueType` 声明类型；OTF（CFF 轮廓）
gs 解析不了字形数据，结果是输出 PDF 里中文变成乱码方块。

## 目录里现有的非字体文件

| 文件 | 作用 |
| --- | --- |
| `cidfmap.local` | Ghostscript 的 GBK 字节字体名 → 真实字体路径映射。**默认指向开源字体**，构建时按你有没有放字体自动改写 |
| `fontconfig-chinese.conf` | 「宋体 / 黑体 / 楷体 / 仿宋」这几个中文名到真实 family 的别名。Sim* 排在开源字体前面，所以放了字体就会优先用它 |
