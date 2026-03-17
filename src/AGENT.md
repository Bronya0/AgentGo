# Mini-Agent

一个学习 OpenClaw 架构的迷你通用 Agent 框架，使用 Go 实现。

领域通用、可靠稳定、简单易用、高度可扩展。

---

## 快速开始

### 编译

```bash
cd src
go build -o agent ./cmd/agent/
```

### 配置

复制 `config.example.yaml` 为 `config.yaml`，填入 LLM API 信息：

```yaml
provider:
  type: openai
  base_url: "https://api.openai.com/v1"
  api_key: "${OPENAI_API_KEY}"    # 支持环境变量展开
  model: "gpt-4o"
```

### 运行

```bash
# 单次对话
./agent -chat "帮我列出当前目录下的所有 Go 文件"

# 启动 HTTP 服务器
./agent

# 带 debug 日志
./agent -v

# 指定配置文件
./agent -config /path/to/config.yaml
```

### 调用 API

```bash
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "你好", "session_id": "user-1"}'
```

响应为 SSE 流（`text/event-stream`），每行格式：

```
data: {"type":"text","text":"你好！"}
data: {"type":"tool_start","tool":"list_dir","args":"{\"path\":\".\"}" }
data: {"type":"tool_end","tool":"list_dir","output":"main.go\ngo.mod\n"}
data: {"type":"done"}
```

---

## 架构

```
┌──────────────────────────────────────────────────────────────┐
│                     cmd/agent/main.go                        │
│  组装所有模块 → Provider + Tools + Hooks + Runner + Gateway  │
└────────────────────────────┬─────────────────────────────────┘
                             │
         ┌───────────────────┼───────────────────┐
         ▼                   ▼                   ▼
   ┌──────────┐      ┌────────────┐      ┌────────────┐
   │ Gateway   │      │   Runner   │      │  CronSvc   │
   │ HTTP SSE  │─────▶│ Agent Loop │◀─────│ 定时触发   │
   └──────────┘      └─────┬──────┘      └────────────┘
                           │
              ┌────────────┼────────────┐
              ▼            ▼            ▼
        ┌──────────┐ ┌──────────┐ ┌──────────┐
        │ Provider │ │  Tools   │ │  Hooks   │
        │ (LLM)   │ │ Registry │ │ (Plugin) │
        └──────────┘ └──────────┘ └──────────┘
```

### 核心循环（Runner）

```
用户消息
  ↓ hooks.ProcessMessage()
  ↓ 追加到 Session
  ↓
  ╔═══════════════════════════════╗
  ║  构建消息列表                  ║
  ║    system prompt              ║
  ║    + skill 注入               ║
  ║    + 历史消息（自动截断）      ║
  ║                               ║
  ║  hooks.BeforeLLMCall()        ║
  ║  ↓                            ║
  ║  provider.Chat() → LLM API   ║
  ║  ↓                            ║
  ║  hooks.AfterLLMCall()         ║
  ║  ↓                            ║
  ║  有 tool_calls?               ║
  ║    是 → 执行工具 → 回到顶部   ║
  ║    否 → 返回文本，结束        ║
  ╚═══════════════════════════════╝
```

---

## 模块说明

### Provider（`internal/provider/`）

LLM 调用层，定义统一的 `Provider` 接口。

| 文件 | 说明 |
|------|------|
| `provider.go` | 核心接口和类型：`Provider`, `Message`, `ToolCall`, `StreamHandler` |
| `openai.go` | OpenAI 兼容客户端，支持流式 SSE、自动重试（指数退避 + jitter）、token 用量日志 |
| `failover.go` | 多 Provider 自动切换，cooldown 机制防止连续失败 |

**支持的服务**（任何 OpenAI 兼容 API）：

- OpenAI
- DeepSeek
- Claude (via proxy)
- Ollama
- vLLM
- 任何兼容 `/v1/chat/completions` 的服务

**Failover 配置示例**：

```yaml
providers:
  - id: primary
    base_url: "https://api.openai.com/v1"
    api_key: "${OPENAI_API_KEY}"
    model: "gpt-4o"
  - id: fallback
    base_url: "https://api.deepseek.com/v1"
    api_key: "${DEEPSEEK_API_KEY}"
    model: "deepseek-chat"
```

当主 provider 遇到 429/5xx 错误时，同一 provider 内先重试 3 次（指数退避），耗尽后自动切换到下一个。失败的 provider 进入 60 秒冷却期。

### Tool（`internal/tool/`）

工具是 Agent 与外部世界交互的接口。

| 文件 | 说明 |
|------|------|
| `tool.go` | `Tool` 结构体、`Registry`（线程安全注册表）、参数提取工具函数 |
| `builtin.go` | 7 个内置工具实现 |

**内置工具**：

| 工具 | 说明 |
|------|------|
| `read_file` | 读取文件内容（64KB 上限） |
| `write_file` | 创建/覆写文件 |
| `edit_file` | 精确字符串替换（适合修改代码） |
| `list_dir` | 列出目录内容 |
| `grep_files` | 跨文件搜索内容（大小写不敏感） |
| `run_command` | 执行 shell 命令（进程组隔离） |
| `web_fetch` | 获取 URL 内容（SSRF 防护） |

**自定义工具**：

```go
registry.Register(tool.Tool{
    Name:        "my_tool",
    Description: "描述工具的功能",
    Parameters:  map[string]any{ /* JSON Schema */ },
    Execute: func(ctx context.Context, args tool.Args) tool.Result {
        // 实现逻辑
        return tool.OK("result")
    },
})
```

### Session（`internal/session/`）

对话会话管理。

- 每个 session 维护完整消息历史
- `Pool` 管理多个并发会话
- JSON 文件持久化（`0600` 权限）
- 自动从磁盘恢复历史会话

### Plugin（`internal/plugin/`）

基于 Hook 的插件扩展系统。

**5 个 Hook 点**：

| Hook | 时机 | 用途 |
|------|------|------|
| `before_llm_call` | LLM 调用前 | 修改消息列表、注入上下文 |
| `after_llm_call` | LLM 调用后 | 日志记录、指标采集 |
| `before_tool_call` | 工具调用前 | 修改参数、权限检查、拦截 |
| `after_tool_call` | 工具调用后 | 审计日志 |
| `on_message` | 用户消息到达时 | 过滤、变换、预处理 |

所有 hook 都内置 panic 恢复，单个 hook 崩溃不会影响 agent 运行。

**编写插件**：

```go
type MyPlugin struct{}

func (p *MyPlugin) Name() string { return "my-plugin" }

func (p *MyPlugin) Register(hooks *plugin.Hooks) {
    hooks.OnAfterToolCall(func(ctx context.Context, name string, result tool.Result) {
        slog.Info("tool called", "tool", name, "error", result.IsError)
    })
}

// 注册
pluginMgr.Register(&MyPlugin{})
```

### Skill（`internal/skill/`）

通过 Markdown 文件定义的能力描述，注入 system prompt。

**目录结构**：

```
skills/
├── git-helper/
│   └── SKILL.md
└── code-review/
    └── SKILL.md
```

**SKILL.md 格式**：

```markdown
---
name: git-helper
description: Git 操作辅助
always: true
requires:
  bins: [git]
---

当用户要求 git 操作时，使用 run_command 工具执行 git 命令...
```

- `always: true` — 始终注入 system prompt
- `requires.bins` — 检查可执行文件是否存在
- `requires.env` — 检查环境变量
- 不满足前置条件的 skill 自动跳过

### Lane（`internal/lane/`）

命令队列，保证同一通道内的任务串行执行。

- `main` — 主对话通道
- `cron` — 定时任务通道
- 每个 lane 独立消费，互不干扰

### Cron（`internal/cron/`）

定时任务调度。

```yaml
crons:
  - id: daily-report
    schedule: "every 24h"
    prompt: "生成今日工作总结"
  - id: health-check
    schedule: "every 30m"
    prompt: "检查所有服务的健康状态"
```

支持 `every Ns`、`every Nm`、`every Nh` 格式。

### Memory（`internal/memory/`）

简易长期记忆系统。

- 基于关键词的文本检索（按匹配词数排序）
- JSON 文件持久化
- 向 agent 暴露 `memory_search` 和 `memory_save` 两个工具

```yaml
memory:
  enabled: true
  dir: ".agent/memory"
```

### Gateway（`internal/gateway/`）

HTTP API 服务器。

| 端点 | 方法 | 说明 |
|------|------|------|
| `/v1/chat` | POST | 发起对话（SSE 流式输出） |
| `/healthz` | GET | 健康检查 |

特性：
- SSE 流式输出
- Bearer token 认证（constant-time 比较）
- 请求体大小限制（1MB）
- Session ID 安全校验
- per-session 串行执行锁
- 空闲 session lock 自动 GC（30 分钟）
- 优雅关闭（等待进行中的请求完成）

### Config（`internal/config/`）

YAML 配置文件加载。

- 环境变量展开：`${VAR}` 或 `$VAR`
- 单 provider 简写和多 provider 列表
- 带有冲突检测和警告日志
- 合理的默认值

---

## 安全特性

| 安全措施 | 说明 |
|----------|------|
| 路径穿越防护 | `safeJoin()` 检查逻辑路径 + symlink 解析 |
| SSRF 防护 | `web_fetch` 阻止访问私有 IP（10.x / 172.16.x / 192.168.x / 127.x / 链路本地） |
| 进程隔离 | `run_command` 使用独立进程组，超时时终止所有子进程 |
| 认证安全 | 使用 `crypto/subtle.ConstantTimeCompare` 防时序攻击 |
| 文件权限 | Session 和 Memory 文件使用 `0600` 权限（仅所有者可读写） |
| 请求限制 | 1MB 请求体上限，防止内存耗尽攻击 |
| 输入校验 | Session ID 正则校验，防止目录穿越 |
| Panic 恢复 | 所有 hook 和工具执行都有 panic 恢复，不会崩溃 |
| 循环检测 | 检测重复的工具调用模式，防止 agent 陷入无限循环 |
| 环境变量 | API Key 支持 `${ENV}` 引用，避免明文存储 |

---

## 与 OpenClaw 的对标

| OpenClaw 概念 | Mini-Agent | 状态 |
|---|---|---|
| pi-embedded-runner 循环 | `runner.Runner.Run()` | 完整 |
| AuthProfile failover + cooldown | `provider.FailoverProvider` | 完整 |
| 重试 + 指数退避 | `openai.Chat()` 内置 3 次重试 | 完整 |
| Skill 注入 system prompt | `skill.LoadDir()` + frontmatter | 完整 |
| CommandQueue / Lane | `lane.Lane` + `Manager` | 完整 |
| Plugin Hook 系统 | `plugin.Hooks`（5 个 hook 点） | 完整 |
| Session 持久化 | `session.Pool` + JSON | 完整 |
| CronService | `cron.Service` | 基础 |
| Memory search | `memory.Store` 关键词检索 | 基础 |
| Gateway SSE | `gateway.Server` — `/v1/chat` | 完整 |
| Context 截断 | `runner.trimHistory()` | 完整 |
| 工具安全沙箱 | `safeJoin()` + SSRF + 进程组 | 完整 |
| tool-loop-detection | `loopDetector` | 完整 |
| Context compaction (LLM 摘要) | — | 未实现 |
| 子 Agent 委托 | — | 未实现 |
| 向量记忆检索 | — | 未实现 |
| 多模态（图片/音频） | — | 未实现 |
| MCP 协议 | — | 未实现 |

---

## 项目结构

```
src/
├── cmd/agent/main.go                 # 入口：组装 + 启动
├── config.example.yaml               # 配置示例
├── go.mod / go.sum                   # 依赖管理
└── internal/
    ├── config/config.go              # 配置加载（YAML + 环境变量展开）
    ├── provider/
    │   ├── provider.go               # Provider 接口 + Message + ToolCall 类型
    │   ├── openai.go                 # OpenAI 兼容客户端（流式 + 重试）
    │   └── failover.go              # 多 Provider 自动切换
    ├── tool/
    │   ├── tool.go                   # Tool 接口 + Registry
    │   └── builtin.go               # 7 个内置工具
    ├── session/session.go            # 会话管理 + Pool + 持久化
    ├── runner/runner.go              # Agent 核心循环 + 循环检测
    ├── plugin/plugin.go              # Hook 系统 + Plugin 接口
    ├── skill/skill.go                # SKILL.md 加载 + frontmatter
    ├── lane/lane.go                  # 命令队列
    ├── cron/cron.go                  # 定时任务
    ├── memory/memory.go              # 文本记忆检索
    └── gateway/gateway.go            # HTTP API (SSE)
```

**外部依赖**：仅 `gopkg.in/yaml.v3`（YAML 解析）。
