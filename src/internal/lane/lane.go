// Package lane 实现命令队列（lane），保证同一 lane 内的任务串行执行。
//
// 类似 OpenClaw 的 CommandLane 概念：
//   - main: 主对话通道（maxConcurrent=1）
//   - cron: 定时任务通道
//   - 自定义通道
package lane

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Command 是一个可执行的命令。
type Command struct {
	ID   string
	Name string
	Fn   func(ctx context.Context) error
}

// Lane 是一个串行命令队列。
type Lane struct {
	name    string
	queue   chan Command
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewLane 创建并启动一个 lane（后台 goroutine 串行消费队列）。
// queueSize 指定缓冲区大小。
func NewLane(name string, queueSize int) *Lane {
	if queueSize <= 0 {
		queueSize = 64
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &Lane{
		name:   name,
		queue:  make(chan Command, queueSize),
		cancel: cancel,
	}
	l.wg.Add(1)
	go l.loop(ctx)
	return l
}

// Enqueue 将命令加入队列。若队列已满则阻塞。
// 支持通过 ctx 取消排队等待。
func (l *Lane) Enqueue(ctx context.Context, cmd Command) error {
	select {
	case l.queue <- cmd:
		slog.Debug("command enqueued", "lane", l.name, "command", cmd.Name)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("enqueue cancelled: %w", ctx.Err())
	}
}

// Stop 关闭 lane 并等待当前任务完成。
func (l *Lane) Stop() {
	l.cancel()
	l.wg.Wait()
}

func (l *Lane) loop(ctx context.Context) {
	defer l.wg.Done()
	for {
		select {
		case cmd := <-l.queue:
			slog.Debug("command executing", "lane", l.name, "command", cmd.Name)
			if err := cmd.Fn(ctx); err != nil {
				slog.Error("command failed", "lane", l.name, "command", cmd.Name, "err", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

// Manager 管理多个命名 lane。
type Manager struct {
	mu    sync.Mutex
	lanes map[string]*Lane
}

// NewManager 创建 lane 管理器。
func NewManager() *Manager {
	return &Manager{lanes: make(map[string]*Lane)}
}

// Get 获取或创建一个命名 lane。
func (m *Manager) Get(name string) *Lane {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.lanes[name]; ok {
		return l
	}
	l := NewLane(name, 64)
	m.lanes[name] = l
	return l
}

// StopAll 关闭所有 lane。
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.lanes {
		l.Stop()
	}
}
