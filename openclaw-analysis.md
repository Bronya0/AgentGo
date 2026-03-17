# OpenClaw 技术栈与 Agent 架构分析

> 基于源码分析，日期：2026-03-18

---

## 一、整体技术栈

| 层次 | 技术 |
|------|------|
| 语言 | TypeScript (ESM)，少量 Go（`opencode-go` 扩展） |
| 运行时 | Node.js 22+，Bun（首选开发/脚本执行） |
| 包管理 | pnpm workspace（monorepo），支持 bun install |
| 核心 Agent 引擎 | `@mariozechner/pi-agent-core`、`@mariozechner/pi-coding-agent`、`@mariozechner/pi-ai` |
| Gateway 服务器 | 自研 WebSocket + HTTP 服务（`src/gateway/`） |
| 测试框架 | Vitest，V8 覆盖率（阈值 70%） |
| Lint/Format | Oxlint + Oxfmt |
| 构建工具 | tsdown（`tsdown.config.ts`） |
| 容器化 | Docker / Podman（多个 Dockerfile） |
| 数据库/向量 | SQLite + sqlite-vec（memory-core），LanceDB（memory-lancedb） |
| 文档站 | Mintlify（docs.openclaw.ai） |

### 项目布局（单体仓库）

```
src/          核心运行时逻辑（CLI、Gateway、Agent、Channel、Cron、Plugins…）
extensions/   80+ 官方插件（Telegram、Discord、Slack、各 AI Provider 等）
packages/     内部 workspace 包（clawdbot、moltbot）
apps/         移动端 / 桌面端（iOS、Android、macOS、shared）
ui/           Web UI
skills/       内置 Skill 定义
```

---

## 二、Agent 核心架构

### 核心运行入口

`src/agents/pi-embedded-runner/run.ts` → `runEmbeddedPiAgent()`（外层循环）  
`src/agents/pi-embedded-runner/run/attempt.ts` → `runEmbeddedAttempt()`（单次 LLM 调用）

**两层结构**：

```
[inbound message]
   ↓
dispatchInboundMessage()          // src/auto-reply/dispatch.ts
   ↓
dispatchReplyFromConfig()
   ↓
enqueueCommandInLane(lane)        // src/process/command-queue.ts（串行化）
   ↓
runEmbeddedPiAgent()              // ★ 外层重试/failover 循环
  ├── resolveAuthProfileOrder()   // 候选 (provider, model, apiKey) 列表
  ├── for (attempt < maxRetries):  // min=32, max=160（随候选数量扩展）
  │     runEmbeddedAttempt()      // ★ 单次 LLM 调用尝试
  │       createAgentSession() + streamSimple()  // @mariozechner/pi-coding-agent
  │       subscribeEmbeddedPiSession()           // 订阅流式输出
  │       onBlockReply() → ReplyDispatcher → Channel.send()
  │
  │   [成功] → 返回结果
  │   [rate_limit/overload] → sleep(backoff) → 切换 profile → 重试
  │   [context_overflow] → compactEmbeddedPiSession() → 重试
  │   [billing/auth] → markAuthProfileFailure() → 下一候选
  └── [全部失败] → 向用户发送错误消息
```

**重试迭代上限**：
```
maxRetries = clamp(
  BASE_RUN_RETRY_ITERATIONS(24) + profileCount * ITERATIONS_PER_PROFILE(8),
  MIN=32, MAX=160
)
```

---

## 三、Skill 注入机制

**文件**：`src/agents/skills/`，入口 `src/agents/skills.ts`

### Skill 来源（按优先级合并）

1. **工作区本地**：`~/.openclaw/skills/`（SKILL.md + frontmatter）
2. **Bundled**：内置到 OpenClaw 安装包中（`bundled-dir.ts`）
3. **Plugin 提供**：通过 `api.registerSkillDir()` 注入
4. **管理安装**：`skills-install.ts` 从远程下载/解压

### 注入流程

```
loadWorkspaceSkillEntries(workspaceDir)
   ↓  加载、解析 frontmatter，过滤不符合 eligibility 的 skill
filterSkillEntries()              // 应用 configPath、allowlist、skillFilter 规则
   ↓
buildWorkspaceSkillsPrompt()      // 生成 Skills 文本块（最多 150 条，30,000 chars）
   ↓
buildSkillsSection()              // 注入 system prompt 的 "## Skills (mandatory)" 区块
```

**System Prompt Skill 指令**（`src/agents/system-prompt.ts`）：

> 每次回复前先扫描 `<available_skills>` 描述；若一个 skill 明确适用，则用 `read` 工具读取其 SKILL.md，然后遵照执行。最多只读一个 skill。

### SKILL.md Frontmatter 字段（`OpenClawSkillMetadata`）

```yaml
---
always: true            # 是否始终加入 system prompt（无需 LLM 选择）
skillKey: my-skill      # 覆盖默认 key
primaryEnv: OPENAI_API_KEY  # 主要环境变量（用于 eligibility 检测）
emoji: 🔧               # 展示图标
homepage: https://...   # Skill 主页
os: [mac, linux]        # 平台限制
requires:
  bins: [git, node]     # 必须存在的二进制
  anyBins: [npm, pnpm]  # 至少一个存在
  env: [GITHUB_TOKEN]   # 必须有值的环境变量
  config: [key.path]    # 必须有值的配置键
install:
  - kind: brew          # brew | node | go | uv | download
    formula: ripgrep
  - kind: node
    package: "@org/tool@latest"
  - kind: go
    module: "github.com/user/tool@latest"
  - kind: download
    url: https://...
    archive: tool.tar.gz
    bins: [tool]
---
```

**调用策略字段**（`invocation` 区块）：
- `userInvocable`：是否可作为 `/skill:name` 命令用户直接调用
- `disableModelInvocation`：禁止 LLM 自主触发（仅用户命令可调用）

### Limits

| 参数 | 默认值 |
|------|--------|
| maxCandidatesPerRoot | 300 |
| maxSkillsLoadedPerSource | 200 |
| maxSkillsInPrompt | 150 |
| maxSkillsPromptChars | 30,000 |
| maxSkillFileBytes | 256,000 |

---

## 四、对接 App / 接收指令

### 输入渠道（`extensions/`）

超过 30 种渠道插件，包括：

- **即时通讯**：Telegram、Discord、Slack、WhatsApp、Signal、iMessage、Line、Feishu（飞书）、微信（通过第三方桥接）、Matrix、Mattermost、MSTeams、IRC、Nostr、Google Chat、NextCloud Talk、Tlon、Zalo、Twitch
- **语音**：voice-call、talk-voice
- **API**：OpenAI 兼容接口（`gateway/openai-http.ts`）、ACP 协议（`src/acp/`）
- **Web 前端**：`src/channel-web.ts`、`src/gateway/server-browser.ts`

### 消息接收路径

```
Channel Plugin (e.g. Telegram)
  拿到 raw message → MsgContext 封装
    ↓
dispatchInboundMessage()
    ↓
[命令检测] commands-registry.ts
  → /new /reset /cancel /help /skill:xxx 等内置命令
  → 未命中命令 → 走 LLM 对话路径
    ↓
finalizeInboundContext()          // 完善渠道、发送者、身份信息
    ↓
createReplyDispatcher()           // 建立回复队列
    ↓
runEmbeddedPiAgent()
```

### Gateway 服务器

`src/gateway/server.impl.ts`：

- WebSocket + HTTP（支持 Tailscale 暴露）  
- Control UI（管理界面），受 CSRF / origin 校验保护
- OpenAI 兼容 API 端点
- 设备配对（`device-pair` 扩展）
- 移动节点（iOS/Android）通过 `server-mobile-nodes.ts` 连接

---

## 五、任务规划

**规划模式**：OpenClaw 不使用独立的 Planner 模块，而是通过**精心构建的 System Prompt** 让 LLM 自主规划。

`buildSystemPromptParams()` → `buildEmbeddedSystemPrompt()` 包含以下区块：

| 区块 | 内容 |
|------|------|
| `## Skills (mandatory)` | Skill 扫描 + 读取指令 |
| `## Memory Recall` | 使用 `memory_search` / `memory_get` 前置回忆 |
| `## Authorized Senders` | owner 身份信息 |
| `## Tooling` | 工具使用规范（exec、edit、web_fetch…） |
| `## Workspace` | 工作区路径、shell 环境 |
| `## Sandbox` | 沙箱路径映射 |
| `## Runtime` | 当前时区、机器名、渠道能力 |
| `## Bootstrap` | CLAUDE.md / bootstrap 文件内容（bootstrap-files.ts） |

### 推理增强

- `ThinkLevel`（off/low/medium/high）：控制扩展思考  
- `ReasoningLevel`：stream 模式实时输出 `<think>` 块  
- `pickFallbackThinkingLevel()`：failover 时自动降级推理级别

### Plugin Hooks

- `before_prompt_build`：可注入额外 prompt  
- `before_agent_start`：可完全替换 prompt / messages

---

## 六、任务调度

### Lane（通道）系统

`src/process/lanes.ts` + `src/process/command-queue.ts`

```ts
enum CommandLane {
  Main    = "main",    // 主对话，maxConcurrent=1
  Cron    = "cron",    // 定时任务，独立通道
  Subagent= "subagent",// 子 agent 并行执行
  Nested  = "nested",  // Cron 内部嵌套调用（防死锁）
}
```

每个 Lane 维护独立队列，`enqueueCommandInLane()` 串行化单 Lane 的任务。  
Session 级 Lane 格式：`session:<sessionKey>`，隔离不同会话。

### 子 Agent（Subagent）

`src/agents/subagent-registry.ts`

- `sessions_spawn` 工具产生子 agent（独立 session）
- `SubagentRegistry` 追踪所有在运行中的子 agent（Map<string, SubagentRunRecord>）
- 支持深度限制、超时、生命周期事件（complete / error / killed）
- `subagent-announce-queue.ts`：子 agent 完成后格式化通知父 agent
- `sessions_yield` 工具：挂起当前 session，等待外部事件触发恢复

### Auth Profile 轮转

`src/agents/auth-profiles.ts`

- 多 API Key 配置 → 自动 failover（rate limit / billing error / overload）
- 冷却（cooldown）机制，避免连续失败同一 key
- `markAuthProfileFailure()` / `markAuthProfileGood()` 更新状态

---

## 七、工具执行（Tool Call）

### 工具集合

`createOpenClawCodingTools()` + `createOpenClawTools()` 组合所有工具：

| 类别 | 工具 |
|------|------|
| 文件系统 | `read`, `write`, `edit`, `apply_patch` |
| 代码执行 | `exec`（bash）, `process`（交互式 PTY） |
| Web | `web_search`, `web_fetch` |
| 多媒体 | `image`, `pdf`, `tts`, `browser` |
| 记忆 | `memory_search`, `memory_get` |
| 会话管理 | `sessions_spawn`, `sessions_send`, `sessions_list`, `sessions_history`, `sessions_yield`, `session_status` |
| 定时任务 | `cron`（add/list/remove/run） |
| 渠道动作 | `message`（发送消息到渠道）, channel-specific actions (Discord moderation, Telegram reactions, Slack, etc.) |
| 其他 | `canvas`, `nodes`, `gateway`, `agents_list`, `subagents` |

### 控制流

```
LLM 输出 tool_call
  ↓
pi-tool-definition-adapter.ts
  before_tool_call hook（插件可 intercept/modify 参数）
    ↓
Tool 函数执行
    ↓
after_tool_call hook（插件可观察结果）
    ↓
tool result → 写回 session transcript
    ↓
session 重新进入下一轮 LLM 调用
```

### 安全策略

- `tool-policy-pipeline.ts`：组合式策略管道
- `tool-fs-policy.ts`：文件系统路径限制（workspace-only 模式）
- `tool-loop-detection.ts`：检测重复 tool call（防无限循环）
- `session-tool-result-guard.ts`：防止 tool result 过大导致 context overflow
- **Exec 审批**：`ExecApprovalManager`（`src/gateway/exec-approval-manager.ts`）
  - 高危命令需要用户确论（可配置 allow/deny list）
  - `bash-tools.exec-approval-request.ts`：向用户发送审批请求
  - `bash-tools.exec-approval-followup.ts`：处理用户回复（允许/拒绝）
  - `node-invoke-system-run-approval.ts`： Nodes 工具调用筛查

---

## 八、结果发送

### 流式输出处理

`subscribeEmbeddedPiSession()` — `src/agents/pi-embedded-subscribe.ts`

```
LLM stream → delta 块
  ↓
EmbeddedBlockChunker     // 按段落/代码块/换行偏好切分
  ↓
[tag 过滤]
  <think> / <thinking>   → reasoning 独立处理
  <final>                → 最终内容标记
  工具 summary            → 插入 tool 调用摘要
  ↓
onBlockReply(payload)    // block 级别回调
  ↓
ReplyDispatcher          // 带排队和 typing 指示器管理
  ↓
Channel.send()           // 各渠道的实际发送逻辑
```

### 回复去重

- `isMessagingToolDuplicateNormalized()`：防止消息工具和 block reply 双发
- `messagingToolSentTexts` 列表追踪已发送内容

### Draft Stream（流式实时输出）

`src/channels/draft-stream-controls.ts` + `draft-stream-loop.ts`

支持流式实时预览的渠道（Telegram / Slack 等）会在 LLM 输出过程中不断编辑同一条消息：

```
LLM stream delta 块
  ↓
FinalizableDraftStreamLoop
  │  throttle=N ms（避免过频 API 调用）
  ├─ update(text) → sendOrEditStreamMessage()
  ├─ flush()       → 发送最终内容
  └─ stop()        → 回差删除草稿消息
```

- `isStopped()` / `isFinal()` 状态机防止竞争改写
- 可删除草稿（消息发送失败时回滚）

### 状态反馈

- Typing 指示器：`typing-lifecycle.ts`，发送中显示"…"
- ACK 反应（emoji）：`ack-reactions.ts`（Telegram/Discord/Slack reaction）
- 状态反应：`status-reactions.ts`（任务进行中/完成/失败）
- `RunStateMachine`：`activeRuns` 计数 + 心跳（60s 间隔）

---

## 九、后台任务

### CronService

`src/cron/service.ts` + `src/cron/service/*.ts`

- **调度算法**：`schedule.ts` 解析 cron 表达式 / `every N分钟,小时,天` / `at HH:MM` / one-shot
- **错误通知**：`delivery.failure-notify.ts`
- **Stagger**：顶部 of hour 分散（stagger.ts），防止大量任务同时触发
- **独立 Agent**：`isolated-agent.ts` — Cron 任务在专属 session 中运行，完成后发送结果到指定频道
- **日志**：`run-log.ts` 记录每次执行历史
- **持久化**：`store.ts` SQLite 存储 job 状态

### 后台维护定时器

`src/gateway/server-maintenance.ts`

- 定期清理孤儿子 agent（`subagent-orphan-recovery.ts`）
- 定期检查 gateway 更新
- 内存 / session 周期清理

### HeartbeatRunner

`src/infra/heartbeat-runner.ts`

- 定时系统事件（`enqueueSystemEvent`），用于唤醒 Cron 检查
- 保持 gateway 活跃连接的心跳机制

### Polls（原生投票）

`src/polls.ts` + `src/poll-params.ts`

支持实现渠道原生投票（Telegram poll、Discord poll）：

```ts
type PollInput = {
  question: string;
  options: string[];         // 至少 2 个选项
  maxSelections?: number;    // 多选数量
  durationSeconds?: number;  // Telegram：5-600s
  durationHours?: number;    // Discord：小时
};
```

- `image` 工具可附带投票选项
- 投票结果通过 channel-specific 事件回调处理

---

## 十、其他 Agent 功能（深度分析）

### 1. Context Engine（可插拔上下文管理）

`src/context-engine/` — 定义 `ContextEngine` 接口，允许插件完全替换 context 装配、压缩和检索逻辑。

**接口方法（完整）**：

| 方法 | 调用时机 | 说明 |
|------|----------|------|
| `bootstrap(sessionId, sessionFile)` | session 初始化时 | 从持久化存储加载历史 context |
| `ingest(message)` | 每条消息写入后 | 单条消息持久化到引擎存储 |
| `ingestBatch(messages)` | 批量写入时（optional） | 一个 turn 批量写入 |
| `assemble(sessionId, tokenBudget)` | 构建请求前 | 从存储中装配 messages + 可选系统提示附加 |
| `compact(sessionId)` | 溢出时（optional） | 引擎自主管理压缩生命周期 |
| `afterTurn(...)` | turn 完成后（optional） | 后台持久化 + 按需触发后台压缩 |
| `prepareSubagentSpawn(...)` | spawn 前（optional） | 子 agent 启动前预处理（含 rollback） |
| `onSubagentEnd(...)` | 子 agent 结束后（optional） | 清理子 agent 持久化状态 |

引擎通过 `info.ownsCompaction=true` 声明自主管理压缩，避免 pi-embedded-runner 重复触发。

**Context Pruning（微型压缩）** — `src/agents/pi-extensions/context-pruning.ts`

- 只修改当次请求的 in-memory context，**不写盘**
- 按工具类型过滤（`ContextPruningToolMatch`），裁剪指定工具的历史 result

---

### 2. 对话历史压缩（Compaction）

`src/agents/compaction.ts` + `pi-embedded-runner/compact.ts`

**触发机制**：

1. `pi-project-settings`（pi-coding-agent 内置）检测 context 溢出 → 调用 `compactEmbeddedPiSession()`
2. 外层 run loop 在 `isLikelyContextOverflowError` 时也强制触发

**算法**：

```
estimateMessagesTokens(messages)      // chars/4 启发式（含 SAFETY_MARGIN=1.2）
    ↓
splitMessagesByTokenShare(messages, parts=2)
  → [chunk1, chunk2]                  // 按 token 份额均分
    ↓
for each chunk:
  generateSummary(chunk, instructions) // 调用 LLM 生成摘要
    ↓
合并多个 partial summary（若 parts > 1）
  → MERGE_SUMMARIES_INSTRUCTIONS 二次调用 LLM
    ↓
新 session: [summary message] + 保留最近 N 条
```

**保留策略（MUST PRESERVE 指令）**：
- 活跃任务及其状态（in-progress/blocked/pending）
- 批处理进度（如 "5/17 items completed"）
- 最后一个用户请求和当前处理状态
- 决策和理由
- TODO / 开放问题 / 约束
- UUID、哈希、令牌、主机名、IP、URL（`IDENTIFIER_PRESERVATION_INSTRUCTIONS`）

**安全超时**：`compaction-safety-timeout.ts`，防止 compaction LLM 调用卡住主流程。

| 常量 | 值 | 说明 |
|------|----|------|
| `BASE_CHUNK_RATIO` | 0.4 | 最大分块比例 |
| `MIN_CHUNK_RATIO` | 0.15 | 最小分块比例 |
| `SAFETY_MARGIN` | 1.2 | token 估算安全系数 |
| `DEFAULT_PARTS` | 2 | 默认分段数 |
| `SUMMARIZATION_OVERHEAD_TOKENS` | 4096 | 摘要 prompt 开销预留 |

---

### 3. Memory（向量记忆系统）

`src/agents/memory-search.ts`、`extensions/memory-core/`、`extensions/memory-lancedb/`

**存储层**：

| 驱动 | 插件 | 说明 |
|------|------|------|
| SQLite + sqlite-vec | memory-core | 默认，轻量，无额外服务依赖 |
| LanceDB | memory-lancedb | 高性能向量库，适合大规模 |

**Embedding 提供商**（`provider` 配置）：

| 提供商 | 默认模型 |
|--------|---------|
| openai | `text-embedding-3-small` |
| gemini | `gemini-embedding-001` |
| voyage | `voyage-4-large` |
| mistral | `mistral-embed` |
| ollama | `nomic-embed-text` |
| local | ONNX（Sherpa，离线） |
| auto | 根据已配置 API key 自动选择 |

**混合检索（Hybrid Search）参数**：

```
vector_weight = 0.7,  text_weight = 0.3
candidate_multiplier = 4          // 先取 N*4 候选再重排
MMR (最大边际相关性): 默认关闭, lambda=0.7
temporal_decay: 默认关闭, half_life=30天
min_score = 0.35,  max_results = 6
```

**分块策略**：tokens=400，overlap=80

**同步触发条件**：
- `onSessionStart`：每次 session 启动
- `onSearch`：搜索时先同步
- `watch`：文件监听（debounce=1500ms）
- 定时：`intervalMinutes`（可配置）
- session delta：累计 bytes ≥ 100,000 或 messages ≥ 50 时同步

**搜索来源**（`sources`）：`["memory"]` 或 `["memory", "sessions"]`（sessions 需开启 experimental）

---

### 4. ACP（Agent Communication Protocol）

`src/acp/`、`extensions/acpx/`、SDK：`@agentclientprotocol/sdk`

ACP 是 OpenClaw 实现的标准化 Agent 间通信协议，允许外部 agent 通过标准接口与 OpenClaw 交互。

**ACP Session 结构**：

```ts
type AcpSession = {
  sessionId: SessionId;    // ACP 协议 ID
  sessionKey: string;      // OpenClaw 内部 session key
  cwd: string;             // 工作目录
  createdAt: number;
  lastTouchedAt: number;
  abortController: AbortController | null;
  activeRunId: string | null;
}
```

**服务端配置**：

| 参数 | 说明 |
|------|------|
| `provenanceMode` | `off` / `meta` / `meta+receipt` — 来源溯源模式 |
| `prefixCwd` | 是否在 prompt 前置工作目录 |
| `sessionCreateRateLimit` | session 创建速率限制（maxRequests/windowMs） |
| `requireExistingSession` | 仅允许连接现有 session |
| `resetSession` | 连接时重置 session |

**核心文件**：
- `acp/translator.ts`：ACP 消息 ↔ OpenClaw 内部格式转换
- `acp/session.ts`：session 生命周期管理
- `acp/persistent-bindings.ts`：ACP 绑定持久化（跨重启保持）
- `acp/control-plane/`：控制平面 API

---

### 5. Plugin / Extension 系统

`src/plugins/`

**注册 API**（`OpenClawPluginApi`）：

| 方法 | 功能 |
|------|------|
| `api.registerTool(factory, opts)` | 注册工具（动态按 session 实例化） |
| `api.registerCli(factory, opts)` | 注册 CLI 子命令 |
| `api.registerHook(name, handler)` | 注册 Hook |
| `api.registerSkillDir(dir)` | 注册 Skill 目录 |
| `api.registerProvider(spec)` | 注册 LLM Provider |
| `api.registerService(service)` | 注册后台 Service（start/stop 生命周期） |
| `api.registerChannelPlugin(plugin)` | 注册消息渠道 |
| `api.registerWebSearchProvider(spec)` | 注册 web 搜索后端 |

**Hook 点完整列表**：

| Hook 名 | 触发时机 |
|---------|---------|
| `before_agent_start` | agent 运行前（可替换 prompt/messages） |
| `before_prompt_build` | prompt 构建前（可注入额外内容） |
| `before_tool_call` | tool 调用前（可修改参数 / 拦截） |
| `after_tool_call` | tool 调用后（可观察结果） |
| `on_message` | 消息处理前（可过滤/变换） |
| `on_session` | session 事件（start/end） |
| `on_subagent` | 子 agent 事件 |
| `gateway_stop` | gateway 停止前 |
| `inbound_claim` | 入站消息 ownership 校验 |
| `wired_hooks_llm` | LLM 调用层 hook |

**Plugin 加载流程**：

```
gateway 启动 → loadGatewayPlugins()
  → 扫描 extensions/*/index.ts（bundled）
  → 扫描 config.plugins[] 配置路径（custom）
  → plugin.register(api) 注册所有能力
  → ensureRuntimePluginsLoaded() 在每次 agent run 前懒初始化
```

---

### 6. 多模型 Failover

`src/agents/model-fallback.ts`、`src/agents/auth-profiles.ts`

**Failover 触发条件**（`FailoverReason`）：

| 原因 | 说明 |
|------|------|
| `rate_limit` | 429 / RESOURCE_EXHAUSTED |
| `billing_error` | 账单/配额错误 |
| `overload` | 服务过载（529 等） |
| `context_overflow` | context 超过 provider 限制 |
| `auth_error` | API key 无效 |
| `timeout` | 请求超时 |

**Failover 流程**：

```
runEmbeddedPiAgent()
  → resolveAuthProfileOrder()      // 候选列表（按优先级 + lastUsed 排序）
  → for each candidate:
      runFallbackAttempt(provider, model)
        → 成功 → markAuthProfileGood()  → 返回结果
        → 失败 → coerceToFailoverError() → markAuthProfileFailure()
                                          → 进入 cooldown
                → sleepWithAbort(backoff) → 尝试下一个候选
```

**退避策略（OVERLOAD_FAILOVER_BACKOFF_POLICY）**：
- initialMs=250, maxMs=1500, factor=2, jitter=0.2

**运行时 Auth 刷新**：
- `RUNTIME_AUTH_REFRESH_MARGIN_MS` = 5分钟 → 提前续期 OAuth token
- 刷新失败重试间隔：60s
- 最小延迟：5s（防止紧密重试）

---

### 7. Bootstrap 文件注入

`src/agents/bootstrap-files.ts`、`src/agents/workspace.ts`

**文件发现顺序**（`loadWorkspaceBootstrapFiles`）：

1. 工作区根目录 `CLAUDE.md`（最高优先级）
2. 配置指定的额外 bootstrap 文件
3. `plugin before_agent_start hook` 注入的动态文件

**Context 模式过滤**：

| contextMode | runKind | 加载的文件 |
|-------------|---------|-----------|
| `full` | 任意 | 全部 |
| `lightweight` | `heartbeat` | 仅 `HEARTBEAT.md` |
| `lightweight` | `cron`/`default` | 空（不加载） |

**预算保护**：

```
analyzeBootstrapBudget(files, maxChars, totalMaxChars)
  → 超出 maxChars 时截断单个文件内容
  → 超出 totalMaxChars 时丢弃低优先级文件
  → 生成 buildBootstrapPromptWarning() 注入 system prompt 告知 LLM 截断情况
```

Bootstrap 缓存：`bootstrap-cache.ts` — 同一 session 内文件内容缓存，避免重复 I/O。

---

### 8. 会话写锁与并发安全

`src/agents/session-write-lock.ts`

**锁文件结构**（JSON）：

```ts
type LockFilePayload = {
  pid?: number;        // 持锁进程 PID
  createdAt?: string;  // 锁获取时间
  starttime?: number;  // /proc/pid/stat field 22（防 PID 复用）
}
```

**超时策略**：

| 常量 | 值 | 说明 |
|------|----|------|
| `DEFAULT_STALE_MS` | 30 分钟 | 锁文件陈旧判定阈值 |
| `DEFAULT_MAX_HOLD_MS` | 5 分钟 | 单次锁最大持有时间 |
| `DEFAULT_WATCHDOG_INTERVAL_MS` | 60 秒 | 锁 watchdog 检查间隔 |
| `DEFAULT_TIMEOUT_GRACE_MS` | 2 分钟 | 超时宽限期 |

**Stale 检测**：
- PID 已不存在 → 陈旧
- starttime 不匹配（PID 被复用）→ 陈旧
- 年龄超过 `STALE_MS` → 陈旧

**信号清理**：监听 SIGINT/SIGTERM/SIGQUIT/SIGABRT，进程退出时自动释放所有持有的锁。

**Session Transcript Repair**（`session-transcript-repair.ts`）：
- 修复 tool_use / tool_result 消息配对错误
- 剔除孤立的 tool_result（没有对应的 tool_use）

---

### 9. OpenAI 兼容 API

`src/gateway/openai-http.ts`、`src/gateway/openresponses-http.ts`

**端点**：

| 端点 | 说明 |
|------|------|
| `POST /v1/chat/completions` | OpenAI Chat Completions 兼容（streaming/非streaming） |
| `POST /v1/responses` | OpenResponses API（`/v1/responses`，新格式） |

**图片支持**：
- 最多 8 张图片（`DEFAULT_OPENAI_MAX_IMAGE_PARTS=8`）
- 最大总大小 20MB（`DEFAULT_OPENAI_MAX_TOTAL_IMAGE_BYTES`）
- 禁止 URL 图片（`allowUrl=false`），只接受 base64

**Body 限制**：20MB（`DEFAULT_OPENAI_CHAT_COMPLETIONS_BODY_BYTES`）

流式响应通过 SSE（`text/event-stream`）推送，支持 `stream=true`。

---

### 10. Diagnostics & Observability（OTel）

`extensions/diagnostics-otel/`

**技术栈**：`@opentelemetry/sdk-node`，完整 OTLP 支持

**导出端点**：

| 信号 | 端点路径 |
|------|---------|
| Traces | `/v1/traces` |
| Metrics | `/v1/metrics` |
| Logs | `/v1/logs` |

**采样器**：`ParentBasedSampler(TraceIdRatioBasedSampler(rate))`，采样率可配置（0~1）。

**Log 传输**：`registerLogTransport()` 桥接 OpenClaw 内部日志 → OTLP Logs，敏感字段自动 `redactSensitiveText()`。

**Diagnostic Events**：`onDiagnosticEvent(handler)` 订阅内部诊断事件并映射为 OTel Spans/Events。

**其他 Observability 组件**：
- `cache-trace.ts`：追踪 Anthropic prompt cache hit/miss
- `anthropic-payload-log.ts`：记录完整 API payload（调试用，含安全过滤）
- `src/logging/subsystem.ts`：分子系统命名空间日志（`createSubsystemLogger("agent/embedded")`）

---

### 11. Sandbox 文件系统隔离

`src/agents/sandbox.ts`

- Docker / Podman exec 沙箱，工作区映射到容器内
- 只读挂载模式（`ro`），保护宿主文件
- PTY 支持（`bash-tools.exec.pty.ts`），交互式命令（vim、python REPL 等）
- `SandboxFsBridge`：跨沙箱文件操作（read/write 代理到容器内）
- `path-policy.ts`：严格路径策略，防止路径穿越

### 12. 设备配对与多设备

`extensions/device-pair/`、`src/pairing/`

- 移动设备通过 QR 扫码配对 gateway（生成临时 token）
- `server-mobile-nodes.ts`：iOS / Android 节点管理
- `NodeRegistry`：注册多个远程节点（分布式 tool 调用执行）

### 13. TTS（文字转语音）

`src/tts/`、`extensions/talk-voice/`

- `tts` 工具在对话中生成语音消息
- Sherpa-ONNX 离线 TTS（技能安装后本地运行）
- `buildTtsSystemPromptHint()`：向 LLM 提示渠道支持 TTS

### 14. 浏览器控制

`src/browser/`、`src/agents/tools/browser-tool.ts`

- `browser` 工具：截图、点击、键盘输入、导航、等待元素
- `sandboxBrowserBridgeUrl`：沙箱内浏览器代理桥接
- `server-browser.ts`：Gateway 集成浏览器控制服务

---

## 十一、架构总结图

```
┌─────────────────────────────────────────────────────────┐
│                   OpenClaw Gateway                       │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────────┐  │
│  │ Channel  │  │  MsgCtx  │  │  CommandQueue (Lane)  │  │
│  │ Plugins  │→ │ Dispatch │→ │  main/cron/subagent   │  │
│  └──────────┘  └──────────┘  └──────────────────────┘  │
│                                        ↓                  │
│  ┌─────────────────────────────────────────────────────┐ │
│  │            pi-embedded-runner / attempt             │ │
│  │  ① build SystemPrompt (Skills + Memory + Tools)    │ │
│  │  ② createAgentSession (pi-coding-agent)             │ │
│  │  ③ streamSimple → LLM API                          │ │
│  │  ④ subscribeEmbeddedPiSession (stream consumer)    │ │
│  │     ├─ text delta → BlockChunker → onBlockReply    │ │
│  │     ├─ tool_call  → execute tool → tool_result     │ │
│  │     └─ compaction → LLM summary → continue         │ │
│  └─────────────────────────────────────────────────────┘ │
│                        ↓ reply                            │
│  ┌──────────┐  ┌──────────────┐  ┌─────────────────────┐ │
│  │  Reply   │  │   Channel    │  │   Background        │ │
│  │Dispatcher│→ │   send()     │  │  CronService        │ │
│  └──────────┘  └──────────────┘  │  SubagentRegistry   │ │
│                                   │  HeartbeatRunner    │ │
│                                   └─────────────────────┘ │
└─────────────────────────────────────────────────────────┘
         ↑ Extensions: 80+ providers, channels, tools
```

---

*分析基于 `src/` 和 `extensions/` 源码，核心引擎为 `@mariozechner/pi-*` npm 包（封装了 LLM streaming + session 管理）。*
