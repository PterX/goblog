# AnqiCMS MCP Server + Eino 开发计划 Checklist

> 决策：mcp-go 作为 MCP 协议实现，Eino v0.8.13 作为 AI 编排必选框架
> 版本基准：Go 1.25 + Eino v0.8.13 + mcp-go

---

## Phase 0: 基础设施（Week 1）

### 0.1 Eino 集成
- [ ] `go get github.com/cloudwego/eino@v0.8.13`
- [ ] `go get github.com/cloudwego/eino-ext/components/model/openai@v0.8.13`
- [ ] 创建 `pkg/ai/eino/` 目录结构
- [ ] 实现 `ChatModel` 对接 OpenAI 兼容接口（复用 config/aiGenerate.go）
- [ ] 验证 ChatModel.Invoke() 和 ChatModel.Stream() 基本调用
- [ ] 集成 Callback 体系（日志追踪）

### 0.2 mcp-go 集成
- [ ] `go get github.com/modelcontextprotocol/go-sdk/mcp`
- [ ] 创建 `pkg/mcp/` 目录结构
- [ ] 实现基础 MCP Server（SSE + HTTP 传输）
- [ ] 实现 MCP 初始化协议（initialize/tool/list/tool/call）
- [ ] 验证 MCP Server 可通过 MCP Inspector 调试

### 0.3 权限桥接层
- [ ] 设计统一的权限校验中间件（供 MCP Tool 调用）
- [ ] 实现 `CheckPermission(role, action)` 函数
- [ ] 创建 `pkg/mcp/auth.go` 统一拦截未认证请求
- [ ] 设计 Tool 执行上下文（携带当前用户角色信息）

---

## Phase 1: 核心内容服务 MCP 化（Week 2-3）

> 将 CMS 最核心的 Archive（文章）和 Category（分类）服务包装为 MCP Tool

### 1.1 文章 CRUD
- [ ] `archive_list` Tool — 分页获取文章列表（支持筛选/排序）
  - [ ] 参数：page, pageSize, categoryId, status, keyword
  - [ ] 返回：分页后的文章列表 JSON
- [ ] `archive_get` Tool — 获取单篇文章详情
  - [ ] 参数：archiveId
  - [ ] 返回：文章完整内容
- [ ] `archive_create` Tool — 创建文章
  - [ ] 参数：title, content, categoryId, tags, cover 等
  - [ ] 返回：新建文章 ID
  - [ ] 集成：自动执行 SEO 分析
- [ ] `archive_update` Tool — 更新文章
  - [ ] 参数：archiveId + 更新字段
  - [ ] 返回：操作结果
- [ ] `archive_delete` Tool — 删除文章（软删除）
  - [ ] 参数：archiveId
  - [ ] 返回：操作结果
- [ ] `archive_publish` Tool — 发布/下架文章
  - [ ] 参数：archiveId, status
  - [ ] 返回：操作结果

### 1.2 分类管理
- [ ] `category_list` Tool — 获取分类列表
  - [ ] 参数：parentId (可选，支持树形结构)
  - [ ] 返回：分类树
- [ ] `category_create` Tool — 创建分类
- [ ] `category_update` Tool — 更新分类
- [ ] `category_delete` Tool — 删除分类
- [ ] `category_detail` Tool — 获取分类详情

### 1.3 标签管理
- [ ] `tag_list` Tool — 获取标签列表
- [ ] `tag_create` Tool — 创建标签
- [ ] `tag_update` Tool — 更新标签
- [ ] `tag_detail` Tool — 获取标签详情

### 1.4 附件管理
- [ ] `attachment_list` Tool — 获取附件列表
- [ ] `attachment_upload` Tool — 上传附件（对接现有 storage 系统）
- [ ] `attachment_delete` Tool — 删除附件
- [ ] `attachment_url` Tool — 获取附件 URL

### 1.5 Eino Compose 层（验证 Graph 能力）
- [ ] 构建 `CreateArticleGraph` — 参数校验 → 内容安全审核 → 保存 → SEO 分析 → 返回
  - [ ] 用 `compose.NewGraph` 定义节点
  - [ ] 用 `compose` 编排 Lambda 节点和并行分支
  - [ ] 测试 Graph 编译和执行
  - [ ] 将 Graph 通过 `graphtool.NewInvokableGraphTool` 暴露为 MCP Tool
- [ ] 构建 `ListArticleGraph` — 检索 → 过滤 → 分页
  - [ ] 验证 Graph 并行处理能力

---

## Phase 2: Admin 管理服务 MCP 化（Week 4-5）

> 将后台管理功能逐步暴露，支持 AI 自主管理 CMS

### 2.1 系统管理
- [ ] `system_info` Tool — 获取系统配置信息
  - [ ] 从 config/anqi.go 和 provider 读取配置
- [ ] `system_status` Tool — 获取系统运行状态
  - [ ] CPU/内存/磁盘/数据库连接状态
- [ ] `setting_get` Tool — 获取网站设置
- [ ] `setting_update` Tool — 更新网站设置
- [ ] `user_list` Tool — 获取管理员列表
- [ ] `user_create` Tool — 创建管理员
- [ ] `user_update` Tool — 更新管理员
- [ ] `user_delete` Tool — 删除管理员
- [ ] `user_role_assign` Tool — 分配角色权限

### 2.2 插件管理
- [ ] `plugin_list` Tool — 获取插件列表
- [ ] `plugin_install` Tool — 安装插件
- [ ] `plugin_enable` Tool — 启用插件
- [ ] `plugin_disable` Tool — 禁用插件
- [ ] `plugin_uninstall` Tool — 卸载插件
- [ ] `plugin_config_get` Tool — 获取插件配置
- [ ] `plugin_config_update` Tool — 更新插件配置

### 2.3 页面管理
- [ ] `page_list` Tool — 获取页面列表
- [ ] `page_create` Tool — 创建页面
- [ ] `page_update` Tool — 更新页面
- [ ] `page_delete` Tool — 删除页面

### 2.4 评论管理
- [ ] `comment_list` Tool — 获取评论列表
- [ ] `comment_approve` Tool — 审核通过评论
- [ ] `comment_delete` Tool — 删除评论
- [ ] `comment_reply` Tool — 回复评论

### 2.5 统计与日志
- [ ] `statistics_overview` Tool — 获取统计概览
  - [ ] 访问量、用户数、文章数等
- [ ] `crond_list` Tool — 获取计划任务列表
- [ ] `crond_trigger` Tool — 手动执行计划任务

---

## Phase 3: Eino Agent 编排（Week 6-7）

> 将 MCP Tool 接入 Eino ADK，构建 CMS Agent

### 3.1 Eino Agent 核心
- [ ] 创建 `pkg/ai/cms_agent.go`
- [ ] 实现 `CMSChatModelAgent`
  - [ ] 集成 Phase 1-2 的所有 MCP Tool
  - [ ] 配置 ToolsNode 实现 ReAct 自动调用
  - [ ] 设置 System Prompt（CMS 管理员角色描述）
- [ ] 实现 `AgentRunner.Run()` — 统一入口
  - [ ] 接收用户自然语言指令
  - [ ] 返回 Agent 决策结果

### 3.2 HITL（人工审批）流程
- [ ] 实现 `PublishApprovalGraph`
  - [ ] 节点：ContentReview → Checkpoint → HumanApprove → Publish
  - [ ] 集成 Eino Interrupt/Resume 机制
  - [ ] 未审批时挂起流程
  - [ ] 审批通过后 Resume
- [ ] 实现 `DraftReviewGraph` — 草稿自动预审
  - [ ] 节点：AI 评分 → 内容建议 → 人工终审

### 3.3 Eino DeepAgent（多代理协作）
- [ ] 实现 `SEOAuditAgent` — SEO 优化子代理
  - [ ] 子代理：标题优化、Meta 标签生成、关键词密度分析
- [ ] 实现 `ContentFormatAgent` — 排版优化子代理
  - [ ] 子代理：Markdown 格式化、图片 Alt 标签补充
- [ ] 配置 `DeepAgent` 协调子代理
- [ ] 测试 DeepAgent 自动拆分复杂任务

### 3.4 Graph 工具化（可被 Agent 调用）
- [ ] 将 `CreateArticleGraph` 包装为 `GraphTool`
- [ ] 将 `ListArticleGraph` 包装为 `GraphTool`
- [ ] 将 `PublishApprovalGraph` 包装为 `GraphTool`
- [ ] 验证 Agent 可以自主选择调用哪些 Graph 工具

---

## Phase 4: 前端集成（Week 8）

> 提供 MCP Client 和 SSE 流式聊天接口

### 4.1 SSE 流式聊天接口
- [ ] 创建 `controller/aiChat.go`
  - [ ] POST `/api/ai/chat` — 发送消息，SSE 流式返回
  - [ ] POST `/api/ai/chat/stream` — 流式对话
  - [ ] GET `/api/ai/chat/history` — 获取对话历史
- [ ] 集成 Eino ChatModelAgent.Stream() 实现 SSE 推送
- [ ] 实现断线重连机制

### 4.2 MCP Client
- [ ] 创建 `pkg/mcp/client.go` — MCP Client 实现
  - [ ] 支持 SSE + Stdio 两种传输方式
  - [ ] 自动管理 MCP Server 连接
  - [ ] 实现 Tool 注册与调用
- [ ] 将 Client 与 AgentRunner 对接

### 4.3 管理面板集成
- [ ] 在 Admin 页面嵌入 AI 助手入口
- [ ] 实现对话界面（Markdown 渲染、代码高亮）
- [ ] 支持指令快捷按钮（"创建文章"、"生成分类"等）
- [ ] 实现 AI 操作预览与确认机制

---

## Phase 5: 记忆系统与增强（Week 9-10）

### 5.1 Eino 记忆组件
- [ ] `go get github.com/cloudwego/eino-ext/components/retriever/*`
- [ ] `go get github.com/cloudwego/eino-ext/components/embedding/*`
- [ ] 配置 Embedding 组件（对接 OpenAI Embedding 或本地模型）
- [ ] 配置 Retriever 组件
- [ ] 实现 `DocumentTransformer` — 文章分块处理

### 5.2 语义检索
- [ ] 实现 CMS 文章向量化索引
  - [ ] 基于文章标题+摘要生成 embedding
  - [ ] 支持增量索引更新
- [ ] 实现语义搜索 Tool — `search_by_semantic`
  - [ ] 自然语言查询 → Embedding → 相似度检索 → 返回结果
- [ ] 接入 Meilisearch/ZincSearch（如支持向量检索）

### 5.3 上下文记忆
- [ ] 实现 `ConversationMemory` — 对话上下文缓存
  - [ ] 最近 N 轮对话历史
  - [ ] 用户偏好记忆
- [ ] 实现 `ContextWindow` — 控制上下文窗口大小

---

## Phase 6: 飞书小龙虾兼容（Week 11-12, 可选）

### 6.1 MCP 协议一致性
- [ ] 验证 MCP Server 完全兼容 MCP 2025-06-18 协议
- [ ] 支持 MCP 资源（Resource）注册
  - [ ] `resource://archive/{id}` — 文章资源
  - [ ] `resource://category/{id}` — 分类资源
- [ ] 支持 MCP Prompt 注册（预设对话模板）

### 6.2 小龙虾 Agent 接入
- [ ] 调研飞书小龙虾 Agent API
- [ ] 实现 `SkillBroker` — 根据意图路由到不同 Agent
  - [ ] 本地 Eino Agent 处理 CMS 管理任务
  - [ ] 小龙虾 Agent 处理复杂创意任务
- [ ] 测试 Agent 间嵌套调用

---

## 技术架构图

```
┌─────────────────────────────────────────────────────────────┐
│                      管理面板 / API Client                   │
└────────────────────────┬────────────────────────────────────┘
                         │ SSE / HTTP
┌────────────────────────▼────────────────────────────────────┐
│                    AnqiCMS MCP Server                        │
│  ┌─────────────────────────────────────────────────────────┐ │
│  │              Eino ADK (Agent 编排层)                     │ │
│  │  ┌─────────────┐  ┌─────────────┐  ┌───────────────┐  │ │
│  │  │ CMSAgent    │  │ DeepAgent   │  │ HITL Runner   │  │ │
│  │  │ (ReAct)     │  │ (子代理)    │  │ (人工审批)    │  │ │
│  │  └──────┬──────┘  └──────┬──────┘  └───────┬───────┘  │ │
│  └─────────┼────────────────┼──────────────────┼─────────┘ │
│            │                │                  │            │
│  ┌─────────▼────────────────▼──────────────────▼─────────┐ │
│  │              Eino Compose (编排层)                      │ │
│  │  ┌──────────┐  ┌──────────┐  ┌───────────────────┐   │ │
│  │  │ Graph    │  │ Workflow │  │ Branch/Parallel   │   │ │
│  │  │ (DAG)    │  │ (线性链) │  │ (条件分支)        │   │ │
│  │  └──────────┘  └──────────┘  └───────────────────┘   │ │
│  └─────────────────────────┬─────────────────────────────┘ │
│                            │ Tool Call                       │
│  ┌─────────────────────────▼─────────────────────────────┐ │
│  │              MCP Tool 层 (mcp-go)                      │ │
│  │  ┌──────┐ ┌──────┐ ┌──────┐ ┌──────┐ ┌───────────┐   │ │
│  │  │Archive│ │User │ │Plugin│ │System│ │Setting   │   │ │
│  │  └──────┘ └──────┘ └──────┘ └──────┘ └───────────┘   │ │
│  └─────────────────────────┬─────────────────────────────┘ │
│                            │ 权限校验 + 业务逻辑             │
│  ┌─────────────────────────▼─────────────────────────────┐ │
│  │              AnqiCMS 业务层                            │ │
│  │  model/ controller/ provider/                          │ │
│  └───────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

---

## 目录规划

```
kandaoni.com/anqicms/
├── pkg/
│   ├── ai/
│   │   ├── eino/
│   │   │   ├── chat_model.go      # ChatModel 对接
│   │   │   ├── agent.go           # Agent 编排
│   │   │   ├── graphs/            # Eino Graph 定义
│   │   │   │   ├── create_article_graph.go
│   │   │   │   ├── list_article_graph.go
│   │   │   │   └── publish_approval_graph.go
│   │   │   ├── agents/            # Agent 定义
│   │   │   │   ├── cms_agent.go
│   │   │   │   ├── seo_agent.go
│   │   │   │   └── format_agent.go
│   │   │   └── memory.go          # 上下文记忆
│   │   └── mcp/
│   │       ├── server.go          # MCP Server 实现
│   │       ├── client.go          # MCP Client
│   │       ├── tools/             # MCP Tool 注册
│   │       │   ├── archive.go
│   │       │   ├── category.go
│   │       │   ├── user.go
│   │       │   ├── plugin.go
│   │       │   └── system.go
│   │       └── auth.go            # 权限桥接
│   └── middleware/
│       └── ai_auth.go             # AI 请求权限中间件
├── controller/
│   └── aiChat.go                  # SSE 流式聊天接口
└── router/
    └── ai.go                      # AI 路由注册
```

---

## 关键风险与应对

| 风险 | 影响 | 应对 |
|------|------|------|
| Eino v0.x Breaking Change | 重构成本 | 锁定版本，抽象 `pkg/ai/` 层做隔离 |
| MCP 协议变更 | 兼容性问题 | 使用 mcp-go 最新版，不直接写协议 |
| 权限越权 | 高危 | 所有 MCP Tool 统一走 `CheckPermission` |
| 大流量 OOM | 性能问题 | 限制 Agent 并发，设置上下文窗口上限 |
| 敏感操作误执行 | 数据安全 | 发布/删除等操作必须 HITL 审批 |

---

## 里程碑

| 里程碑 | 时间 | 交付物 |
|--------|------|--------|
| **M1: 基础框架** | Week 1 | Eino + mcp-go 可运行 Demo |
| **M2: 核心 MCP** | Week 3 | 文章/分类/标签 CRUD 可调用 |
| **M3: Agent 就绪** | Week 7 | CMS Agent 可自主管理内容 |
| **M4: 全功能** | Week 10 | 记忆系统 + 语义搜索 |
| **M5: 生产可用** | Week 12 | 安全加固 + 性能测试 + 文档 |
