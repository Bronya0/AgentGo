# AgentGo

A miniature general-purpose Agent framework learning from OpenClaw's architecture, implemented in Go.

**Domain-generic • Reliable & Stable • Simple & Intuitive • Highly Extensible**

## Quick Start

### Build

```bash
cd src
go build -o agent ./cmd/agent/
```

### Configure

Copy `src/config.example.yaml` to `src/config.yaml` and fill in your LLM API credentials:

```yaml
provider:
  type: openai
  base_url: "https://api.openai.com/v1"
  api_key: "${OPENAI_API_KEY}"    # Environment variable expansion supported
  model: "gpt-4o"
```

### Run

```bash
# Single message mode
./agent -chat "List all .go files in the current directory"

# Start HTTP server
./agent

# With debug logging
./agent -v

# Specify config file
./agent -config /path/to/config.yaml
```

### API Usage

```bash
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "Hello", "session_id": "user-1"}'
```

Response is SSE stream (`text/event-stream`):

```
data: {"type":"text","text":"Hello!"}
data: {"type":"tool_start","tool":"list_dir","args":"{\"path\":\".\"}" }
data: {"type":"done"}
```

## Architecture Highlights

### Core Loop

```
User Message
  ↓
Process Message (hooks)
  ↓
Append to Session
  ↓
Build Messages (system prompt + skills + history)
  ↓
Call LLM (with streaming & retry)
  ↓
Execute Tools (if requested)
  ↓
Loop or Return Result
```

### Key Components

| Component | Purpose |
|-----------|---------|
| **Provider** | OpenAI-compatible LLM API client with automatic retry & failover |
| **Tools** | 7 built-in tools (read/write/edit files, run commands, web fetch, grep, etc.) |
| **Session** | Conversation history management with JSON persistence |
| **Runner** | Agent core loop with tool execution & loop detection |
| **Plugin** | Hook-based extension system (5 hooks) |
| **Skill** | SKILL.md files for capability injection |
| **Gateway** | HTTP SSE API server |
| **Memory** | Long-term memory with keyword search |
| **Cron** | Background task scheduling |
| **Lane** | Command queue for serial execution |

## Features

### Security
- ✅ Path traversal prevention (+ symlink resolution)
- ✅ SSRF protection (blocks private IP addresses)
- ✅ Constant-time authentication
- ✅ Process group isolation for safe command execution
- ✅ Panic recovery in all hooks and tools
- ✅ Input validation (session ID regex, request size limits)
- ✅ File permission hardening (0600 for sensitive files)

### Reliability
- ✅ Automatic retry with exponential backoff (3 retries, 500ms-5s)
- ✅ Multi-provider failover + cooldown
- ✅ Tool loop detection (stops infinite patterns)
- ✅ Graceful shutdown (waits for in-flight requests)
- ✅ Session lock GC (prevents memory leaks)

### Extensibility
- ✅ Plugin system with 5 hook points
- ✅ Custom tool registration
- ✅ Environment variable expansion in config
- ✅ Skill injection from SKILL.md files
- ✅ Multiple storage backends ready (vector DB, Redis)

## Documentation

See [src/AGENT.md](src/AGENT.md) for comprehensive technical documentation including:
- Module specifications
- Configuration examples
- Tool API reference
- Security architecture
- Comparison with OpenClaw

## Project Structure

```
.
├── README.md                      # This file
├── openclaw-analysis.md           # Analysis notes from OpenClaw
├── src/
│   ├── go.mod / go.sum            # Go dependencies
│   ├── AGENT.md                   # Full documentation
│   ├── config.example.yaml        # Configuration template
│   ├── cmd/agent/main.go          # Entry point
│   └── internal/                  # Core packages
│       ├── provider/              # LLM API client
│       ├── tool/                  # Tool system
│       ├── session/               # Session management
│       ├── runner/                # Agent loop
│       ├── plugin/                # Hook system
│       ├── skill/                 # Skill loader
│       ├── gateway/               # HTTP server
│       ├── config/                # Configuration
│       ├── memory/                # Long-term memory
│       ├── cron/                  # Task scheduler
│       └── lane/                  # Command queue
```

## Dependencies

**Only one external dependency**: `gopkg.in/yaml.v3` for YAML parsing.

```
go 1.22
require gopkg.in/yaml.v3 v3.0.1
```

## Use Cases

- **Code Assistant**: Read, search, edit code with `grep_files` and `edit_file` tools
- **Task Automation**: Schedule cron jobs, chain multiple operations via hooks
- **Integration Hub**: Extend via plugins to add custom logic or external service calls
- **Research Agent**: Memory system for accumulating knowledge across conversations
- **Development Tool**: Multimodal prompt building from files + web content

## Comparison with OpenClaw

Mini-Agent implements the essential architecture from OpenClaw:

| Concept | Status |
|---------|--------|
| Core Loop (embedding runner) | ✅ Complete |
| Provider Failover | ✅ Complete |
| Retry with backoff | ✅ Complete |
| Skill Injection | ✅ Complete |
| Command Queue (lanes) | ✅ Complete |
| Plugin/Hook System | ✅ Complete (5 hooks) |
| Session Persistence | ✅ Complete |
| Cron Service | ✅ Basic |
| Memory Search | ✅ Keyword-based |
| Gateway (HTTP SSE) | ✅ Complete |
| Loop Detection | ✅ Complete |
| LLM Context Compaction | ⏳ Future |
| Sub-Agent Delegation | ⏳ Future |
| Vector DB Integration | ⏳ Future |

## Contributing

This project is open to contributions. Please feel free to:
- Report bugs and security issues
- Suggest features
- Improve documentation
- Add new tools or plugins

## License

MIT

---

**Getting Started**: Read the [config example](src/config.example.yaml), check your LLM API key, and run the agent!

For detailed information, see [src/AGENT.md](src/AGENT.md).
