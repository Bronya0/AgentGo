# AgentGo

一个学习 OpenClaw 架构的迷你通用 Agent 框架，使用 Go 实现。

**领域通用 · 可靠稳定 · 简单易用 · 高度可扩展**

## 快速开始

### 编译

```bash
cd src
go build -o agent ./cmd/agent/
```

### 配置

复制 `src/config.example.yaml` 为 `src/config.yaml`，填入你的 LLM API 信息：

```yaml
# 单 provider 模式
provider:
  type: openai
  base_url: "https://api.openai.com/v1"
  api_key: "${OPENAI_API_KEY}"    # 支持环境变量展开
  model: "gpt-4o"

# 多模型路由模式（按复杂度自动选择）
providers:
  - id: fast
    model: gpt-4o-mini
    tier: fast           # 简单问答
  - id: balanced
    model: gpt-4o
    tier: balanced       # 常规任务（默认）
  - id: powerful
    model: o1-preview
    tier: powerful       # 复杂推理
```

### 运行

```bash
# 单次对话模式
./agent -chat "帮我列出当前目录下的所有 Go 文件"

# 启动 HTTP 服务器
./agent

# 带 debug 日志
./agent -v

# 指定配置文件
./agent -config /path/to/config.yaml
```

### API 调用

```bash
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "你好", "session_id": "user-1"}'
```

响应为 SSE 流（`text/event-stream`）：

```
data: {"type":"text","text":"你好！"}
data: {"type":"tool_start","tool":"list_dir","args":"{\"path\":\".\"}" }
data: {"type":"done"}
```
### WebSocket 实时通信

```javascript
const ws = new WebSocket('ws://localhost:8080/v1/ws');
ws.onopen = () => {
  ws.send(JSON.stringify({ message: '你好', session_id: 'user-1' }));
};
ws.onmessage = (e) => {
  const data = JSON.parse(e.data);
  // { type: 'text' | 'tool_start' | 'tool_end' | 'done', ... }
  console.log(data);
};
```

### MCP 协议

Agent 实现了 [Model Context Protocol](https://modelcontextprotocol.io/) 服务端，支持三种传输模式：

```yaml
mcp:
  enabled: true
  mode: http         # stdio | http
```

- **stdio 模式**：通过 stdin/stdout 进行 JSON-RPC 2.0 通信，适配 VS Code Copilot 等 MCP 客户端
- **HTTP SSE 模式**：`GET /mcp` 建立 SSE 流 + `POST /mcp` 发送请求
- **Streamable HTTP 模式**：`POST /mcp` 直接返回 JSON 响应

支持的 MCP 方法：`initialize`、`tools/list`、`tools/call`、`ping`

### Web UI 仪表盘

启动服务后访问 `http://localhost:8080/ui` 即可使用内置 Web UI：

- **对话界面**：通过 WebSocket 实时交互，支持 Markdown 渲染、工具调用可视化
- **系统概览**：查看版本信息、已注册工具列表、活跃会话数
- **会话管理**：浏览所有会话、切换对话、重置会话

Web UI 使用纯 HTML/CSS/JS 内嵌到 Go 二进制中，无需额外静态文件部署。

### 对话导出/导入

```bash
# 导出为 JSON（可精确还原）
curl "http://localhost:8080/v1/session/export?session_id=user-1&format=json" -o chat.json

# 导出为 Markdown（人类可读）
curl "http://localhost:8080/v1/session/export?session_id=user-1&format=markdown" -o chat.md

# 导入 JSON 对话
curl -X POST "http://localhost:8080/v1/session/import?session_id=user-1" \
  -H "Content-Type: application/json" -d @chat.json
```
## 架构概览

### 核心循环

```
用户消息（HTTP / 企微 / 钉钉 / 飞书）
  ↓
ACL 权限检查
  ↓
消息预处理（hooks）
  ↓
追加到会话历史
  ↓
构建消息列表（system prompt + skills + 历史）
  ↓
调用 LLM（流式输出 + 自动重试）
  ↓
ACL 工具级权限检查 → 执行工具调用（若有）
  ↓
循环或返回结果
```

### 核心组件

| 组件 | 说明 |
|------|------|
| **Provider** | OpenAI 兼容的 LLM API 客户端，含自动重试、failover 和多模型路由 |
| **Tools** | 25+ 工具（文件操作、命令执行、Git、Webhook、定时任务、沙箱、子 Agent 等） |
| **Session** | 对话历史管理，支持 JSON 持久化、Markdown/JSON 导出导入 |
| **Runner** | Agent 核心循环，含并行工具执行、上下文压缩、子 Agent 委托、循环检测 |
| **Plugin** | 基于 Hook 的扩展系统（5 个钩子点） |
| **Skill** | 通过 SKILL.md 文件注入能力描述 |
| **Gateway** | HTTP SSE + WebSocket 双协议 API 服务器 |
| **Channel** | 聊天平台渠道适配（企微 / 钉钉 / 飞书） |
| **MCP** | Model Context Protocol 服务端（stdio / HTTP SSE / Streamable HTTP） |
| **ACL** | 用户级 + 工具级访问控制 |
| **Memory** | 长期记忆，支持关键词检索和向量语义搜索 |
| **Sandbox** | 进程级沙箱隔离（临时目录 + 最小环境变量 + 输出限制） |
| **Cron** | 定时任务调度（支持固定间隔和每日定时，运行时动态增删） |
| **RateLimit** | 令牌桶限流 + 每日 Token 配额管理 |
| **WebUI** | 内嵌 Web 仪表盘（对话界面 + 系统概览 + 会话管理） |
| **Lane** | 命令队列，保证串行执行 |

## 工具列表

### 内置工具（8 个）

| 工具 | 功能 |
|------|------|
| `read_file` | 读取文件内容（沙箱限制 + symlink 防护） |
| `write_file` | 写入文件（自动创建目录） |
| `edit_file` | 精确字符串替换编辑 |
| `list_dir` | 列出目录内容 |
| `grep_files` | 搜索文件内容（大小写不敏感、glob 过滤） |
| `run_command` | 执行 Shell 命令（命令黑名单 + 环境变量过滤 + 输出限制） |
| `run_command_sandboxed` | 沙箱命令执行（临时目录隔离 + 最小环境变量 + 输出限制） |
| `web_fetch` | 获取网页内容（SSRF 防护） |

### Git 工具（4 个）

| 工具 | 功能 |
|------|------|
| `git_pull` | 拉取最新代码（支持指定分支） |
| `git_log` | 查看提交历史（支持 since/until/author/max_count） |
| `git_diff` | 查看代码差异（支持指定 ref 和文件路径） |
| `git_show` | 查看指定提交的完整内容 |

### 通知工具（1 个）

| 工具 | 功能 |
|------|------|
| `webhook_notify` | 发送 Webhook 通知（兼容企微机器人/钉钉/飞书/Slack） |

### 定时任务工具（3 个）

| 工具 | 功能 |
|------|------|
| `cron_add` | 对话中动态添加定时任务 |
| `cron_list` | 列出所有活跃的定时任务 |
| `cron_remove` | 删除指定定时任务 |

### 记忆工具（2 个）

| 工具 | 功能 |
|------|------|
| `memory_add` | 保存信息到长期记忆（支持标签分类） |
| `memory_search` | 基于关键词搜索记忆（支持向量语义搜索） |

### 子 Agent 工具（2 个）

| 工具 | 功能 |
|------|------|
| `delegate_task` | 将子任务委托给独立 Agent（独立会话上下文） |
| `parallel_tasks` | 并行执行最多 5 个子任务（自动合并结果） |

## 聊天渠道

支持通过企业聊天平台直接与 Agent 交互，实现双向通信。

### 企业微信

- 回调路径：`/channel/wecom/callback`
- 消息加密：AES-256-CBC + PKCS7
- 签名验证：SHA1
- 配置项：`corp_id`、`agent_id`、`secret`、`token`、`encoding_aes_key`

**配置步骤：**
1. 在[企业微信管理后台](https://work.weixin.qq.com)创建自建应用
2. 在「接收消息」中设置 URL 为 `http://your-server:8080/channel/wecom/callback`
3. 在 config.yaml 填入 `corp_id`、`agent_id`、`secret`、`token`、`encoding_aes_key`

### 钉钉

- 回调路径：`/channel/dingtalk/callback`
- 签名验证：HmacSHA256
- 回复方式：优先 sessionWebhook，过期后自动回退到工作通知 API
- 配置项：`app_key`、`app_secret`、`robot_code`（可选）

**配置步骤：**
1. 在[钉钉开放平台](https://open-dev.dingtalk.com)创建企业内部应用
2. 开启机器人能力，配置消息接收地址
3. 在 config.yaml 填入 `app_key` 和 `app_secret`

### 飞书

- 回调路径：`/channel/feishu/callback`
- URL 验证：自动响应 challenge
- 消息加密：AES-256-CBC（可选）
- 回复方式：通过 `tenant_access_token` 调用消息 API
- 配置项：`app_id`、`app_secret`、`verification_token`、`encrypt_key`（可选）

**配置步骤：**
1. 在[飞书开放平台](https://open.feishu.cn)创建企业自建应用
2. 开启机器人能力，订阅 `im.message.receive_v1` 事件
3. 配置事件请求地址为 `http://your-server:8080/channel/feishu/callback`
4. 在 config.yaml 填入相关参数

### 共通机制

- 所有渠道共享 Runner + Session 池
- 异步消息处理（先返回 200，后台处理并推送回复）
- Access Token 自动缓存与刷新（过期前 5 分钟预刷新）
- 每个用户独立会话（自动从 `platform-userID` 映射）

## 访问控制（ACL）

三层权限模型：

| 层级 | 说明 |
|------|------|
| **用户级** | 白名单 (`allow_users`) / 黑名单 (`deny_users`) 控制谁能使用 Agent |
| **工具级** | `deny_tools` 限制非管理员使用危险工具（如 `run_command`、`write_file`） |
| **管理员** | `admins` 列表中的用户拥有全部权限，不受工具限制 |

用户标识格式统一为 `platform:userID`，例如：
- `wecom:ZhangSan`
- `dingtalk:staff123`
- `feishu:ou_abcdef`

配置示例：

```yaml
acl:
  enabled: true
  default_policy: deny         # 默认拒绝，仅白名单用户可用
  admins:
    - "wecom:admin001"
  allow_users:
    - "wecom:user001"
    - "dingtalk:staff123"
  deny_tools:
    - "run_command"            # 普通用户不能执行命令
    - "write_file"             # 普通用户不能写文件
```

## 安全特性

### 命令执行安全

- **命令黑名单**：正则匹配拦截危险命令模式：
  - 系统破坏：`rm -rf /`、`format C:`、`mkfs`、`dd of=/dev/`、`shutdown`、`reboot`
  - 反弹 Shell：`nc -e`、`ncat -e`、`socat exec`、`bash -i >&`、`/dev/tcp/`
  - 权限提升：`sudo`、`su -`、`runas`、`chmod +s`
  - Fork 炸弹：`:(){ :|:& };:`、`%0|%0`
  - 注册表破坏：`reg delete HKLM`、`bcdedit`
- **环境变量过滤**：子进程不会继承含 `_KEY`、`_SECRET`、`_TOKEN`、`_PASSWORD` 等敏感环境变量
- **输出限制**：stdout 48KB / stderr 16KB 缓冲上限
- **审计日志**：所有命令执行均记录到 slog
- **进程隔离**：Unix 上使用进程组隔离 + SIGKILL 强制终止；Windows 上通过 CommandContext 管理

### 文件操作安全

- **沙箱限制**：所有文件操作限制在 `workspace_dir` 内
- **路径穿越防护**：`../` 和 symlink 逃逸均被拦截
- **敏感文件权限**：Session 文件以 `0600` 权限写入

### 网络安全

- **SSRF 防护**：`web_fetch` 工具阻止访问私有/内网 IP 地址
- **认证**：Gateway API 支持 Bearer Token 认证
- **常量时间比较**：Token 验证使用 `crypto/subtle.ConstantTimeCompare`

### 运行时安全

- **Panic 恢复**：所有 Hook 和工具执行均有 defer recover
- **循环检测**：工具调用循环自动检测并终止
- **LLM 超时**：所有 Provider 请求带超时上下文
- **工具超时**：每个工具调用 5 分钟超时保护

## 定时任务

支持两种调度格式：

```yaml
crons:
  - id: daily-review
    schedule: "daily 19:00"     # 每天 19:00 执行
    prompt: "执行 Code Review..."

  - id: health-check
    schedule: "every 30m"       # 每 30 分钟执行
    prompt: "检查系统状态..."
```

运行时还可通过对话动态管理：
- 对 Agent 说「每天早上 9 点提醒我开站会」→ Agent 调用 `cron_add` 创建任务
- 对 Agent 说「取消那个提醒」→ Agent 调用 `cron_remove` 删除任务

## 功能特性一览

### 智能调度
- **多模型路由**：根据任务复杂度自动选择 fast/balanced/powerful 模型
- **LLM 上下文压缩**：token 达到 70% 上限时自动通过 LLM 摘要压缩早期对话
- **并行工具执行**：多个 tool_calls 自动并发执行，线程安全
- **子 Agent 委托**：自动拆解复杂任务，委托独立 Agent 并行执行
- **对话导出/导入**：支持 JSON（精确还原）和 Markdown（人类可读）两种格式

### 协议 & 通信
- **HTTP SSE**：流式文本输出
- **WebSocket**：全双工实时通信（纯标准库 RFC 6455 实现）
- **MCP 协议**：Model Context Protocol 服务端，支持 stdio / HTTP SSE / Streamable HTTP
- **聊天渠道**：企业微信、钉钉、飞书渠道适配

### 安全隔离
- **进程沙箱**：临时目录隔离 + 最小环境变量 + 输出限制
- **向量记忆**：支持 Embedding 语义搜索，cosine 相似度匹配，自动降级到关键词搜索

### 可靠性
- 指数退避自动重试（3 次重试，500ms-5s）
- 多 Provider failover + 冷却机制
- 工具调用循环检测（阻止无限循环）
- 优雅关闭（等待进行中的请求完成）
- Session Lock 自动 GC（防止内存泄漏）
- **速率限制**：令牌桶算法 per-IP 限流 + 每日 Token 配额管理

### 可扩展性
- 插件系统（5 个 Hook 点）
- 自定义工具注册
- 配置文件环境变量展开
- SKILL.md 能力注入
- **内置 Web UI**：开箱即用的对话界面 + 系统概览 + 会话管理
- 跨平台支持（Windows / Linux / macOS）
- 聊天渠道适配器（可扩展更多平台）

## 项目结构

```
src/
├── cmd/agent/main.go              # 程序入口
├── config.example.yaml            # 配置模板
├── go.mod / go.sum                # Go 依赖
├── skills/                        # Skill 定义
│   └── code-review/SKILL.md       # Code Review 技能
└── internal/                      # 核心包
    ├── acl/acl.go                 # 访问控制列表
    ├── channel/                   # 聊天渠道适配层
    │   ├── channel.go             # 通用接口 + Handler
    │   ├── wecom/                 # 企业微信适配器
    │   │   ├── wecom.go           # 回调处理、消息解密
    │   │   └── token.go           # access_token 管理
    │   ├── dingtalk/              # 钉钉适配器
    │   │   └── dingtalk.go        # 回调处理、签名验证
    │   └── feishu/                # 飞书适配器
    │       └── feishu.go          # 事件订阅、消息解密
    ├── config/config.go           # 配置加载（YAML + 环境变量展开）
    ├── cron/cron.go               # 定时任务调度
    ├── gateway/
    │   ├── gateway.go             # HTTP SSE 网关 + 路由注册
    │   └── websocket.go           # WebSocket 实时通信（RFC 6455）
    ├── lane/lane.go               # 命令队列
    ├── mcp/mcp.go                 # MCP 协议服务端（JSON-RPC 2.0）
    ├── memory/
    │   ├── memory.go              # 长期记忆（关键词检索）
    │   └── vector.go              # 向量记忆（Embedding + 余弦相似度）
    ├── plugin/plugin.go           # Hook 插件系统
    ├── provider/                  # LLM Provider
    │   ├── provider.go            # 接口定义
    │   ├── openai.go              # OpenAI 兼容客户端
    │   ├── failover.go            # 多 Provider 故障转移
    │   └── router.go              # 多模型智能路由
    ├── ratelimit/ratelimit.go     # 令牌桶限流 + Token 配额
    ├── runner/
    │   ├── runner.go              # Agent 核心循环
    │   └── subagent.go            # 子 Agent 委托（单任务 + 并行）
    ├── sandbox/sandbox.go         # 进程级沙箱隔离
    ├── session/
    │   ├── session.go             # 会话管理
    │   └── export.go              # 对话导出/导入（JSON + Markdown）
    ├── skill/skill.go             # Skill 加载器
    ├── webui/                     # Web UI 仪表盘
    │   ├── webui.go               # 路由 + API
    │   └── index.go               # 内嵌 HTML 单页应用
    └── tool/                      # 工具系统
        ├── tool.go                # 接口 + 注册表
        ├── builtin.go             # 8 个内置工具 + 安全工具函数
        ├── exec_unix.go           # Unix 命令执行（进程组隔离）
        ├── exec_windows.go        # Windows 命令执行
        ├── git.go                 # 4 个 Git 工具
        ├── notify.go              # Webhook 通知工具
        └── cron.go                # 3 个定时任务管理工具
```

## 依赖

**仅一个外部依赖**：`gopkg.in/yaml.v3`（YAML 解析）。

```
go 1.22
require gopkg.in/yaml.v3 v3.0.1
```

## 与 OpenClaw 对比

| 概念 | 状态 |
|------|------|
| 核心循环（embedding runner） | ✅ 已完成 |
| Provider Failover | ✅ 已完成 |
| 指数退避重试 | ✅ 已完成 |
| Skill 注入 | ✅ 已完成 |
| 命令队列（lanes） | ✅ 已完成 |
| 插件 Hook 系统 | ✅ 已完成（5 个钩子） |
| 会话持久化 | ✅ 已完成 |
| 定时任务 | ✅ 已完成（固定间隔 + 每日定时 + 动态管理） |
| 记忆检索 | ✅ 已完成（关键词匹配） |
| HTTP SSE 网关 | ✅ 已完成 |
| 循环检测 | ✅ 已完成 |
| Git 工具集 | ✅ 已完成（4 个工具） |
| Webhook 通知 | ✅ 已完成 |
| 聊天渠道（企微/钉钉/飞书） | ✅ 已完成 |
| 访问控制（ACL） | ✅ 已完成（用户级 + 工具级） |
| 命令执行安全加固 | ✅ 已完成（命令黑名单 + 环境变量过滤） |
| SSRF 防护 | ✅ 已完成 |
| Code Review 技能 | ✅ 已完成 |
| 跨平台命令执行 | ✅ 已完成 |
| LLM 上下文压缩 | ✅ 已完成（70% 阈值自动摘要） |
| 并行工具执行 | ✅ 已完成（goroutine + WaitGroup） |
| 多模型路由策略 | ✅ 已完成（fast/balanced/powerful 自动分级） |
| 对话导出/导入 | ✅ 已完成（JSON + Markdown） |
| MCP 协议支持 | ✅ 已完成（stdio / HTTP SSE / Streamable HTTP） |
| 子 Agent 委托 | ✅ 已完成（单任务 + 并行，最多 5 个子 Agent） |
| WebSocket 实时通信 | ✅ 已完成（纯标准库 RFC 6455） |
| 向量语义记忆 | ✅ 已完成（Embedding + 余弦相似度 + 自动降级） |
| 进程沙箱隔离 | ✅ 已完成（临时目录 + 最小环境变量） |
| Web UI 仪表盘 | ✅ 已完成（对话 + 概览 + 会话管理） |
| 速率限制 | ✅ 已完成（令牌桶 + Token 配额） |

## 许可证

MIT
