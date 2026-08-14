# CODEBUDDY.md — gocrux 分身边界声明

> 本文件是 gocrux 仓库的长期记忆文件。新对话开始时，应首先读取本文件以快速重建项目上下文。

## 项目边界声明（最高优先级）

heims 和 gocrux 两个仓库已拆分为两个独立的 AI 分身分别维护。**本分身只负责 gocrux**，heims 的代码修改由另一个分身负责。

1. **本分身只维护 gocrux 源项目**（`F:\labvoyage\go_project\gocrux`），不直接修改 heims 的任何代码（包括 heims 的 `vendor/github.com/Huey1979/gocrux`）。
2. heims 侧发现的 gocrux bug，会以 bug 报告形式写入 gocrux 的 `doc/bug_report.md`，由**本分身负责修复 + commit**。
3. **commit 由本分身执行，push 由用户执行**。
4. 修复完成后，heims 侧由**另一个分身执行** `go get` + `go mod vendor` 同步。
5. **服务器部署由用户本人负责**，任何分身都不做部署操作。

> 记忆中含 heims 项目细节的条目已标注「适用于 heims 分身」，仅供 heims 分身参考，本分身不得执行其中 heims 侧操作。

---

## 项目简介

gocrux 是 heims 项目依赖的 Go 后端框架库（`github.com/Huey1979/gocrux`），提供：

- **Handler 层**：`handler/` 自动生成 REST 接口（List/Get/Create/Update/Delete/GetByCode/ListVersions/EditVersion/Activate/DeleteByFK 等），URL query 参数自动转为过滤条件
- **Service 层**：`service/` 通用 CRUD 服务 + 版本化、草稿箱、关键字搜索、级联删除等
- **Repository 层**：`repository/` 双数据库支持（MySQL/GORM + MongoDB），`ListFilters` 结构化过滤
- **工具链**：`tools/gentity` 实体生成器

## Bug 处理工作流

1. heims 分身发现 gocrux bug → 写入 `doc/bug_report.md`（含编号、现象、根因、修复方案、验证方法）
2. 本分身读取 bug 报告 → 修复代码 → `go build ./... && go vet ./... && go test ./...` 验证
3. 本分身执行 `git commit`（commit message 用临时文件 `-F` 方式写入，避免 cmd 引号/编码问题）
4. 用户负责 `git push`
5. heims 分身负责 `go get github.com/Huey1979/gocrux@<commit_hash>` + `go mod vendor` + `go build ./...` 同步

## 关键约定

- `doc/` 目录已被 `.gitignore` 排除，**严禁**以任何方式将其纳入 Git（禁止 `git add doc/`、禁止改 .gitignore 移除）
- `.codebuddy/` 是工具数据目录，不提交
- `config.yaml` 含敏感信息，已排除
- commit message 含中文时：写临时文件 + `git commit -F`，避免 cmd.exe 多行引号被拆分 pathspec、GBK 编码乱码；乱码仅为终端显示问题，存储为 UTF-8 正常
- 修改框架功能后同步更新 `README.md`（如分页参数等文档）
