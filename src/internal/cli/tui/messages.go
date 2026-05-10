package tui

import (
	"github.com/bronya/mini-agent/internal/provider"
	"github.com/bronya/mini-agent/internal/runner"
)

// ---- tea.Msg 类型 ----

// streamChunkMsg 封装 Runner 产生的流式事件。
type streamChunkMsg struct {
	chunk runner.StreamChunk
}

// streamDoneMsg 标记一次 Run() 调用已结束。
type streamDoneMsg struct {
	err error
}

// usageDeltaMsg 来自 Provider 的 token 用量增量。
type usageDeltaMsg struct {
	usage provider.Usage
}

// tickMsg 用于驱动 spinner 动画。
type tickMsg struct{}

// approvalRequestMsg 工具执行需要用户批准。
type approvalRequestMsg struct {
	toolName string
	command  string
	respond  chan<- bool
}

// approvalDoneMsg 内部消息：批准流程结束。
type approvalDoneMsg struct{}
