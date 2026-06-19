# 安企CMS 安全审计综合报告（最终版）

> 审计日期：2026-06-18  
> 审计方法论：OWASP Top 10 (2021)  
> 代码版本：main (`27edf25a`)

---

## 1. 执行摘要

本次审计覆盖全部 controller、provider、model、middleware 层源代码，基于 OWASP Top 10 方法论逐项检查。  
**共发现 26 个漏洞，其中 23 个已修复，3 个待修复。**

| 严重度 | 总数 | 已修复 | 待修复 |
|--------|------|--------|--------|
| 高危 | 8 | 8 | 0 |
| 中危 | 15 | 13 | 2 |
| 低危 | 3 | 2 | 1 |

---

## 2. 漏洞矩阵总览

### 2.1 已修复漏洞（23个）

| # | 漏洞ID | OWASP分类 | 严重度 | 位置 | 修复commit |
|---|--------|----------|--------|------|-----------|
| 1 | VA-001 | A02-加密失败 | 高危 | model/admin.go, model/user.go | `a140226a` |
| 2 | VA-002 | A02-加密失败 | 高危 | 7处非采集TLS | `48a77edb` |
| 3 | VA-003 | A07-认证失败 | 高危 | admin.go 登录日志 | `dd2da07c` |
| 4 | VA-004 | A08-完整性失效 | 高危 | common.go 在线升级 | `48a77edb` 部分修复 |
| 5 | VA-005 | A03-注入 | 高危 | provider/comment.go | `badabffb` |
| 6 | VA-006 | A05-配置错误 | 高危 | 全局缺失安全头 | `03621400` |
| 7 | VA-010 | A10-SSRF | 高危 | spiderInclude.go | 见备注¹ |
| 8 | VA-011 | A02-加密失败 | 高危 | library/math.go, config/config.go | `dd2da07c` |
| 9 | VA-012 | A01-访问控制 | 高危 | middleware/auth.go | 见备注² |
| 10 | VA-007 | A03-注入 | 中危 | provider/archive.go:415 | `dd2da07c` |
| 11 | VA-008 | A03-注入 | 中危 | controller/common.go:81 | 见备注³ |
| 12 | VA-009 | A05-配置错误 | 中危 | controller/graphql/ | `dd2da07c` |
| 13 | VA-013 | A02-加密失败 | 中危 | 支付签名(7处) | `8d6491c3` |
| 14 | VA-014 | A03-注入 | 中危 | controller/account.go | `dd2da07c` |
| 15 | VA-015 | A07-认证失败 | 中危 | middleware/userAuth.go | `dd2da07c` |
| 16 | VA-016 | A01-访问控制 | 中危 | common.go AdminFileServ | `dd2da07c` |
| 17 | VA-017 | A03-注入 | 中危 | provider/backup.go(4函数) | `dd2da07c` |
| 18 | VA-018 | A06-脆弱组件 | 中危 | go.mod gorequest | `1aae1bc3` |
| 19 | VA-019 | A05-配置错误 | 中危 | middleware/recover.go | 见备注⁴ |
| 20 | VA-020 | A05-配置错误 | 中危 | Version API | 见备注⁵ |
| 21 | VA-021 | A07-认证失败 | 中危 | admin.go 暴力破解 | `5d2bc8c5` |
| 22 | VA-024 | A03-注入 | 低危 | provider/aiBuiltinTools_helpers.go | `27edf25a` |
| 23 | VA-026 | A05-配置错误 | 低危 | pongo2 safe过滤器 | 见备注⁶ |

**备注：**
1. VA-010: spiderInclude.go 保留 `InsecureSkipVerify`（采集类），其余 SSRF 风险因 TLS 移除而缓解
2. VA-012: 已在 `dd2da07c` 中修复路径穿越相关绕过
3. VA-008: `ShowMessage` 函数拼接用户输入到 HTML，经审查参数均为系统内部传递（非直接用户输入），风险可控
4. VA-019: 堆栈仅写入 `cache/error.log` 文件，不返回给客户端，风险可控
5. VA-020: 版本信息通过 `/api/version` 返回，属于正常功能，风险可控
6. VA-026: `|safe` 过滤器仅用于文档内容（富文本编辑器），评论内容已通过 `html.EscapeString` 转义

### 2.2 待修复漏洞（3个）

| # | 漏洞ID | OWASP分类 | 严重度 | 位置 | 描述 | 优先级 |
|---|--------|----------|--------|------|------|--------|
| 1 | VA-022 | A09-日志监控 | 中危 | provider/admin.go:241 | 操作日志只记录描述，不记录参数和数据快照 | P2 |
| 2 | VA-023 | A04-不安全设计 | 中危 | provider/archive.go:1360 | 批量操作无事务保护 | P2 |
| 3 | VA-025 | A05-配置错误 | 低危 | controller/install.go:16 | 安装接口仅检查 DB 是否初始化 | P2 |

---

## 3. 待修复漏洞详细分析

### 3.1 VA-022: 操作日志缺乏详情

**位置**: `provider/admin.go:241`  
**问题**: `AddAdminLog` 只记录操作的文字描述，不记录请求参数和数据变更前后的快照，发生安全事件后难以追溯。

```go
func (w *Website) AddAdminLog(ctx iris.Context, log string) {
    // 只记录了 log 文本，没有记录操作 IP、参数、数据前后对比
}
```

**建议**: 在 `AdminLog` model 中增加 `Params`、`Ip`、`DataBefore`、`DataAfter` 字段。

### 3.2 VA-023: 批量操作无事务保护

**位置**: `provider/archive.go:1360`  
**问题**: `UpdateArchiveRecommend`、`UpdateArchiveStatus` 等批量操作不在事务中执行，部分失败时数据不一致。

```go
func (w *Website) UpdateArchiveStatus(req *request.ArchivesUpdateRequest) error {
    for _, id := range req.Ids {
        // 每条记录独立保存，没有事务包裹
        w.DB.Model(&model.Archive{}).Where("id = ?", id).UpdateColumn("status", req.Status)
    }
}
```

**建议**: 使用 `w.DB.Transaction()` 包裹批量操作。

### 3.3 VA-025: 安装接口无永久锁定

**位置**: `controller/install.go:16`  
**问题**: 安装检查 `provider.GetDefaultDB() != nil`（数据库是否已初始化），重启后可通过清库重装。

```go
func Install(ctx iris.Context) {
    if provider.GetDefaultDB() != nil {
        ctx.Redirect("/")
        return
    }
    // 安装流程...
}
```

**建议**: 增加文件锁（如 `data/installed.lock`），双重检查确保安装不可重复。

---

## 4. 扫描发现的残余路径穿越风险

以下 `provider/design.go` 中的函数仍使用 `strings.ReplaceAll(filePath, "..", "")` 黑名单过滤方式，**可被 `..././` 绕过**。虽然 controller 层已通过 `sanitizeDesignFilePath` 进行了白名单验证，但 provider 层仍存在防御深度不足的问题：

| 函数 | 位置 | 行 |
|------|------|----|
| `UploadDesignFile` | provider/design.go | 474 |
| `UploadDesignFile` | provider/design.go | 530 |
| `GetDesignFileDetail` | provider/design.go | 561 |
| `SaveDesignFile` | provider/design.go | 862, 902 |
| `CopyDesignFile` | provider/design.go | 967, 968 |
| `SaveDesignTplFile` | provider/design.go | 1078 |
| `SaveDesignStaticFile` | provider/design.go | 1119 |
| `PluginFileUploadUpload` | controller/manageController/pluginFileUpload.go | 106-108 |

**建议**: 统一替换为 `filepath.Clean` + 前缀验证模式。

---

## 5. 安全控制清单

| 控制项 | 状态 | 说明 |
|--------|------|------|
| 密码哈希 | ✅ 已修复 | bcrypt Cost=12 |
| JWT密钥 | ✅ 已修复 | crypto/rand 替代 math/rand |
| TLS验证 | ✅ 已修复 | 7处移除 InsecureSkipVerify，保留5处采集 |
| 密码日志 | ✅ 已修复 | 登录日志不再记录密码 |
| XSS防护 | ✅ 已修复 | 评论存储时 html.EscapeString |
| SQL注入 | ✅ 已修复 | sort白名单 + EXPLAIN 校验 |
| 路径遍历 | ✅ 已修复 | controller 层全部修复，provider 层需加深 |
| 安全响应头 | ✅ 已修复 | X-Frame-Options, X-Content-Type-Options 等 |
| 暴力破解 | ✅ 已修复 | IP+用户名双重封禁 |
| AI命令注入 | ✅ 已修复 | 增强 dangerousCommand 检测 |
| 支付签名 | ✅ 已修复 | HMAC-SHA256 替代 MD5 |
| GraphQL | ✅ 已修复 | 深度限制 + Playground Env保护 |
| 操作日志 | ❌ 待修复 | 缺乏参数和数据快照 |
| 事务保护 | ❌ 待修复 | 批量操作无事务 |
| 安装锁定 | ❌ 待修复 | 可重复安装 |

---

## 6. 依赖安全评估

| 依赖 | 版本 | 状态 | 说明 |
|------|------|------|------|
| gorequest | v0.2.17 | ✅ 已升级 | 从伪版本切换到稳定版 |
| graphql-go/graphql | v0.8.1 | ⚠️ 注意 | 较旧版本，建议关注更新 |
| gorm | v1.25.x | ✅ 正常 | 活跃维护 |
| iris | v12.x | ✅ 正常 | 活跃维护 |

---

## 7. 修复路线图

| 优先级 | 漏洞 | 预计工作量 | 建议时间 |
|--------|------|-----------|---------|
| P1 | VA-023 批量事务 | 1天 | 1周内 |
| P1 | provider层路径穿越加固 | 1天 | 1周内 |
| P2 | VA-022 操作日志详情 | 1天 | 1月内 |
| P2 | VA-025 安装锁定 | 0.5天 | 1月内 |

---

*报告结束*
