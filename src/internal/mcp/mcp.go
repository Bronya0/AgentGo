// Package mcp 实现 Model Context Protocol (MCP) 服务端。
//
// MCP 是一种标准协议，允许 LLM 应用（如 VS Code Copilot、Claude Desktop）
// 通过统一接口调用外部工具和资源。
//
// 支持两种传输方式：
//   - stdio: 通过标准输入/输出通信（适合本地集成）
//   - SSE:   通过 HTTP Server-Sent Events 通信（适合远程调用）
//
// MCP JSON-RPC 2.0 消息格式：
//   请求:  {"jsonrpc":"2.0","id":1,"method":"...","params":{...}}
//   响应:  {"jsonrpc":"2.0","id":1,"result":{...}}
//   通知:  {"jsonrpc":"2.0","method":"...","params":{...}}
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bronya/mini-agent/internal/tool"
)

// --- JSON-RPC 2.0 types ---

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"` // string | number | null
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string     `json:"jsonrpc"`
	ID      any        `json:"id,omitempty"`
	Result  any        `json:"result,omitempty"`
	Error   *rpcError  `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// --- MCP Protocol types ---

type initializeParams struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    any        `json:"capabilities"`
	ClientInfo      clientInfo `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"inputSchema"`
}

type callToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type toolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// --- Server ---

// Server 是 MCP 服务端。
type Server struct {
	tools       *tool.Registry
	serverName  string
	serverVer   string
	initialized atomic.Bool
}

// NewServer 创建 MCP 服务端。
func NewServer(tools *tool.Registry, serverName, serverVersion string) *Server {
	if serverName == "" {
		serverName = "mini-agent"
	}
	if serverVersion == "" {
		serverVersion = "1.0.0"
	}
	return &Server{
		tools:      tools,
		serverName: serverName,
		serverVer:  serverVersion,
	}
}

// handleRequest 处理单个 JSON-RPC 请求。
func (s *Server) handleRequest(ctx context.Context, req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "initialized":
		// 客户端确认初始化完成（通知，无需响应）
		return jsonRPCResponse{}
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	case "ping":
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	default:
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: "method not found: " + req.Method},
		}
	}
}

func (s *Server) handleInitialize(req jsonRPCRequest) jsonRPCResponse {
	s.initialized.Store(true)
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    s.serverName,
				"version": s.serverVer,
			},
		},
	}
}

func (s *Server) handleToolsList(req jsonRPCRequest) jsonRPCResponse {
	tools := s.tools.List()
	defs := make([]toolDefinition, 0, len(tools))
	for _, t := range tools {
		schema := t.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		defs = append(defs, toolDefinition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"tools": defs},
	}
}

func (s *Server) handleToolsCall(ctx context.Context, req jsonRPCRequest) jsonRPCResponse {
	var params callToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32602, Message: "invalid params: " + err.Error()},
		}
	}

	t, ok := s.tools.Get(params.Name)
	if !ok {
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32602, Message: "unknown tool: " + params.Name},
		}
	}

	args := tool.Args(params.Arguments)
	if args == nil {
		args = make(tool.Args)
	}

	slog.Info("mcp: tools/call", "tool", params.Name)
	result := t.Execute(ctx, args)

	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: toolResult{
			Content: []contentBlock{{Type: "text", Text: result.Content}},
			IsError: result.IsError,
		},
	}
}

// --- stdio 传输 ---

// ServeStdio 通过标准输入/输出处理 MCP 请求（行分隔的 JSON-RPC）。
func (s *Server) ServeStdio(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	// 允许较大的消息（1MB）
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var mu sync.Mutex
	writeResponse := func(resp jsonRPCResponse) {
		if resp.ID == nil && resp.Result == nil && resp.Error == nil {
			return // 通知消息无需响应
		}
		mu.Lock()
		defer mu.Unlock()
		data, _ := json.Marshal(resp)
		fmt.Fprintf(w, "%s\n", data)
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			writeResponse(jsonRPCResponse{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32700, Message: "parse error"},
			})
			continue
		}

		resp := s.handleRequest(ctx, req)
		writeResponse(resp)
	}

	return scanner.Err()
}

// --- HTTP SSE 传输 ---

// SSEHandler 返回处理 MCP over HTTP SSE 的 http.HandlerFunc。
// 客户端通过 POST 发送 JSON-RPC 请求，通过 GET SSE 流接收消息。
func (s *Server) SSEHandler() http.HandlerFunc {
	type sseClient struct {
		ch     chan []byte
		done   chan struct{}
	}

	var (
		mu      sync.Mutex
		clients = make(map[string]*sseClient)
		nextID  int64
	)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// SSE 流端点
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "SSE not supported", http.StatusInternalServerError)
				return
			}

			clientID := strconv.FormatInt(atomic.AddInt64(&nextID, 1), 10)
			client := &sseClient{
				ch:   make(chan []byte, 64),
				done: make(chan struct{}),
			}

			mu.Lock()
			clients[clientID] = client
			mu.Unlock()

			defer func() {
				mu.Lock()
				delete(clients, clientID)
				mu.Unlock()
				close(client.done)
			}()

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")

			// 发送 endpoint 事件让客户端知道 POST 地址
			postURL := r.URL.Path + "?client_id=" + clientID
			fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", postURL)
			flusher.Flush()

			for {
				select {
				case data := <-client.ch:
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
					flusher.Flush()
				case <-r.Context().Done():
					return
				}
			}
		} else if r.Method == http.MethodPost {
			// JSON-RPC 请求端点
			clientID := r.URL.Query().Get("client_id")

			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, "read error", http.StatusBadRequest)
				return
			}

			var req jsonRPCRequest
			if err := json.Unmarshal(body, &req); err != nil {
				resp := jsonRPCResponse{
					JSONRPC: "2.0",
					Error:   &rpcError{Code: -32700, Message: "parse error"},
				}
				respData, _ := json.Marshal(resp)

				// 如果有 SSE 客户端，通过 SSE 发送
				if clientID != "" {
					mu.Lock()
					c := clients[clientID]
					mu.Unlock()
					if c != nil {
						select {
						case c.ch <- respData:
						default:
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(respData)
				return
			}

			resp := s.handleRequest(r.Context(), req)
			respData, _ := json.Marshal(resp)

			// 通过 SSE 流推送响应
			if clientID != "" {
				mu.Lock()
				c := clients[clientID]
				mu.Unlock()
				if c != nil {
					select {
					case c.ch <- respData:
					default:
						slog.Warn("mcp: SSE client buffer full", "client", clientID)
					}
				}
			}

			w.Header().Set("Content-Type", "application/json")
			w.Write(respData)
		} else {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}
}

// RegisterRoutes 注册 MCP HTTP 路由到 ServeMux。
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	handler := s.SSEHandler()
	// MCP 标准路径
	mux.HandleFunc("/mcp", handler)
	// 也支持显示 capabilities 的 JSON 端点
	mux.HandleFunc("/mcp/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		info := map[string]any{
			"name":            s.serverName,
			"version":         s.serverVer,
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"tools":           len(s.tools.List()),
		}
		json.NewEncoder(w).Encode(info)
	})
}

// --- Streamable HTTP 传输（MCP 2025 规范） ---

// StreamableHTTPHandler 返回无状态的 Streamable HTTP 处理器。
// 客户端 POST JSON-RPC，服务端直接在 HTTP 响应中返回。
func (s *Server) StreamableHTTPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32700, Message: "parse error"},
			})
			return
		}

		// 对于通知（无 id），返回 202 Accepted
		if req.ID == nil && req.Method == "initialized" {
			s.initialized.Store(true)
			w.WriteHeader(http.StatusAccepted)
			return
		}

		resp := s.handleRequest(r.Context(), req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
