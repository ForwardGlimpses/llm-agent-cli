# 分支与版本发布

本项目采用 **主干 + 版本发布分支**，不维护长期 `develop`。`main` 始终可发布，
功能开发走短命分支，功能冻结后才切 `release/vX.Y`。

当前基线：`v0.1.0`（tag 指向 `main`）。

## 分支模型

```text
main        始终可发布，受保护
  │
  ├── feat/v0.2-config-file       功能，短命
  ├── fix/v0.2-token-count        修复，短命
  │
  ├── release/v0.2                功能冻结后切出，只修 bug / 改版本
  │      └── tag v0.2.0
  │
  └── hotfix/v0.2.1-crash         从 v0.2.0 tag 切出，紧急修复
         └── tag v0.2.1
```

| 分支 | 从哪来 | 合到哪 | 用途 | 生命周期 |
| --- | --- | --- | --- | --- |
| `main` | — | — | 始终可发布 | 永久 |
| `feat/v0.2-<topic>` | `main` | `main` | 新功能 | 几天 |
| `fix/v0.2-<topic>` | `main` | `main` | 普通 bug | 几天 |
| `release/v0.2` | `main` | `main` | 冻结、RC、版本号、CHANGELOG | 到发版 |
| `hotfix/v0.2.1-<topic>` | `v0.2.0` tag | `main` + `release/v0.2` | 紧急修复 | 几小时 |
| `docs/<topic>` | `main` | `main` | 文档 | 短命 |
| `chore/<topic>` | `main` | `main` | CI、依赖、重构 | 短命 |

命名规则：

- 分支里只写版本线 `v0.2`，不写完整 patch；
- 完整版本号只用在 tag 上，例如 `v0.2.0`；
- 主题用小写 kebab-case：`feat/v0.3-context-budget`。

## 版本规则

`v1.0.0` 之前：

| 变更 | 版本位 | 示例 |
| --- | --- | --- |
| bugfix，无行为变化 | PATCH | `0.2.0` → `0.2.1` |
| 新功能或 breaking change | MINOR | `0.2.0` → `0.3.0` |
| 预发布 | 后缀 | `0.3.0-rc.1` |

`v1.0.0` 之后遵循标准 SemVer：breaking → MAJOR，feature → MINOR，fix → PATCH。

版本号只有一个来源：[`internal/version`](../internal/version/version.go)。

```go
var (
    Version = "0.1.0"
    Commit  = "dev"
    Date    = "unknown"
)
```

发布构建注入：

```powershell
go build -ldflags `
  "-X agentcli/internal/version.Version=0.2.0 -X agentcli/internal/version.Commit=$(git rev-parse --short HEAD) -X agentcli/internal/version.Date=$(Get-Date -Format yyyy-MM-dd)" `
  -o bin/agent.exe ./cmd/agent
```

发版检查：**tag 去掉 `v` 后必须等于 `version.Version`**。

## 一个版本的完整流程

以 `v0.2.0` 为例。

### 1. 开发

```powershell
git switch main
git pull
git switch -c feat/v0.2-config-file

git add .
git commit -m "feat(config): load model profiles from config file"
git push -u origin feat/v0.2-config-file
```

在 GitHub 开 PR 合回 `main`。

### 2. 功能冻结

```powershell
git switch main
git pull
git switch -c release/v0.2

# 只允许：修 bug、更新 CHANGELOG、改版本号
# Version 改为 0.2.0-rc.1，CHANGELOG 增加 0.2.0 段落
git commit -am "chore(release): v0.2.0-rc.1"
git tag -a v0.2.0-rc.1 -m "v0.2.0-rc.1"
git push -u origin release/v0.2
git push origin v0.2.0-rc.1
```

### 3. 正式发布

```powershell
git switch main
git pull
git merge --no-ff release/v0.2
git tag -a v0.2.0 -m "v0.2.0"
git push origin main
git push origin v0.2.0
```

然后在 GitHub 上基于 `v0.2.0` 创建 Release，内容从 `CHANGELOG.md` 复制。
发布后删除 `release/v0.2`。

### 4. 紧急修复

```powershell
git switch -c hotfix/v0.2.1-token-overflow v0.2.0
# 修复 + 测试
git commit -am "fix(usage): clamp cached tokens to prompt"
git tag -a v0.2.1 -m "v0.2.1"
git push origin hotfix/v0.2.1-token-overflow
git push origin v0.2.1

git switch main
git merge --no-ff hotfix/v0.2.1-token-overflow
git push origin main
```

如果仍在维护 `release/v0.2`，把修复 cherry-pick 回去。

## 发布检查清单

- [ ] `gofmt -l .` 无输出
- [ ] `go vet ./...` 通过
- [ ] `go test ./... -count=1` 通过
- [ ] `CHANGELOG.md` 已更新，Breaking 已标注
- [ ] `internal/version.Version` 与 tag 一致
- [ ] README / 文档已同步
- [ ] annotated tag 已创建并推送
- [ ] GitHub Release 已创建

## GitHub 保护规则

对 `main`：

- 禁止直接 push；
- 必须通过 PR；
- 必须通过 CI（`.github/workflows/ci.yml`）；
- 禁止 force push 和删除分支。

对 tag `v*`：只允许管理员删除。

## 分支清理

功能合并后删除：

```powershell
git branch -d feat/v0.2-config-file
git push origin --delete feat/v0.2-config-file
```

发版后删除 `release/v0.2`；`hotfix/*` 合并后同样删除。
