package tui

import (
	"context"
	"sync"

	"github.com/bronya/mini-agent/internal/runner"
	"github.com/bronya/mini-agent/internal/session"
	"github.com/bronya/mini-agent/internal/tool"
	tea "github.com/charmbracelet/bubbletea"
)

// engine 把 Runner 的流式事件桥接到 bubbletea Program。
// 每次用户提交 prompt：
//
//	1. 开 goroutine 调用 Runner.Run()
//	2. Runner 的 handler 把 StreamChunk 推进 chunkCh
//	3. tea.Cmd listenNext() 从 chunkCh 取一个 chunk → 返回 streamChunkMsg
//	4. Update() 收到 msg → 重新调度 listenNext() → 循环
//
// 设计要点：
//   - chunkCh 缓冲 128，防止 Runner 阻塞
//   - 完成时发 streamDoneMsg，Update() 据此退出 listening 循环
//   - cancelFn 暴露给外层，用于 ESC ESC 取消
type engine struct {
	runner  *runner.Runner
	pool    *session.Pool

	mu       sync.Mutex
	chunkCh  chan runner.StreamChunk
	doneCh   chan error
	cancelFn context.CancelFunc

	// approval 通道：工具需要批准时，engine 把请求丢到 approvalReq，
	// TUI Update() 读到后显示对话框，用户回答后 send 回 channel
	approvalReq  chan approvalRequestMsg
	approvalResp chan bool
}

func newEngine(r *runner.Runner, pool *session.Pool) *engine {
	return &engine{
		runner:       r,
		pool:         pool,
		approvalReq:  make(chan approvalRequestMsg, 1),
		approvalResp: make(chan bool, 1),
	}
}

// approvalFn 返回一个 ExecApprovalFn 用于注入 Runner。
func (e *engine) approvalFn() runner.ExecApprovalFn {
	return func(ctx context.Context, toolName string, args tool.Args) (bool, error) {
		command, _ := args["command"].(string)
		if command == "" {
			command = toolName
		}
		req := approvalRequestMsg{
			toolName: toolName,
			command:  command,
			respond:  e.approvalResp,
		}
		select {
		case e.approvalReq <- req:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		select {
		case ok := <-e.approvalResp:
			return ok, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

// start 启动一次对话。返回 tea.Cmd 用于开始监听 chunk channel。
func (e *engine) start(parent context.Context, sessionID, input string) tea.Cmd {
	e.mu.Lock()
	e.chunkCh = make(chan runner.StreamChunk, 128)
	e.doneCh = make(chan error, 1)
	ctx, cancel := context.WithCancel(parent)
	e.cancelFn = cancel
	chunkCh := e.chunkCh
	doneCh := e.doneCh
	e.mu.Unlock()

	go func() {
		defer close(chunkCh)
		sess := e.pool.Get(sessionID)
		err := e.runner.Run(ctx, sess, input, func(c runner.StreamChunk) {
			select {
			case chunkCh <- c:
			case <-ctx.Done():
			}
		})
		_ = sess.Save()
		doneCh <- err
		close(doneCh)
	}()

	return e.listenChunk()
}

// listenChunk 从 chunkCh 读一个事件，包成 tea.Msg 返回。
// Update 每次收到 streamChunkMsg 后应再次调度 listenChunk。
func (e *engine) listenChunk() tea.Cmd {
	return func() tea.Msg {
		e.mu.Lock()
		chunkCh := e.chunkCh
		doneCh := e.doneCh
		e.mu.Unlock()

		if chunkCh == nil {
			return nil
		}

		chunk, ok := <-chunkCh
		if !ok {
			// channel 关闭 → 读 doneCh 获取 err
			var err error
			if doneCh != nil {
				err = <-doneCh
			}
			return streamDoneMsg{err: err}
		}
		return streamChunkMsg{chunk: chunk}
	}
}

// listenApproval 监听工具批准请求。
func (e *engine) listenApproval() tea.Cmd {
	return func() tea.Msg {
		req, ok := <-e.approvalReq
		if !ok {
			return nil
		}
		return req
	}
}

// cancel 取消当前运行的对话。
func (e *engine) cancel() {
	e.mu.Lock()
	c := e.cancelFn
	e.mu.Unlock()
	if c != nil {
		c()
	}
}

// reset 清理状态，为下一轮对话做准备。
func (e *engine) reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.chunkCh = nil
	e.doneCh = nil
	e.cancelFn = nil
}
