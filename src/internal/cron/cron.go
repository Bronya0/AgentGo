// Package cron 实现简易定时任务调度。
//
// 支持标准 cron 表达式的子集和简易的 "every Nm/Nh" 语法。
// 每个 job 定时触发时，向指定的 callback 发送 prompt，由外部负责执行 agent 对话。
package cron

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Job 描述一个定时任务。
type Job struct {
	ID       string
	Schedule string        // "every 5m", "every 1h", "0 * * * *" 等
	Prompt   string        // 触发时发送给 agent 的 prompt
	interval time.Duration // 解析后的间隔
}

// Callback 是定时任务触发时的回调。
type Callback func(ctx context.Context, job Job)

// Service 管理和调度定时任务。
type Service struct {
	jobs     []Job
	callback Callback
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewService 创建 cron 服务。
func NewService(callback Callback) *Service {
	return &Service{callback: callback}
}

// Add 添加一个定时任务。
func (s *Service) Add(id, schedule, prompt string) error {
	interval, err := parseSchedule(schedule)
	if err != nil {
		return fmt.Errorf("invalid schedule %q: %w", schedule, err)
	}
	s.jobs = append(s.jobs, Job{
		ID:       id,
		Schedule: schedule,
		Prompt:   prompt,
		interval: interval,
	})
	return nil
}

// Start 启动所有定时任务。
func (s *Service) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	for _, job := range s.jobs {
		s.wg.Add(1)
		go s.runJob(ctx, job)
	}
	slog.Info("cron service started", "jobs", len(s.jobs))
}

// Stop 停止所有定时任务。
func (s *Service) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

func (s *Service) runJob(ctx context.Context, job Job) {
	defer s.wg.Done()
	ticker := time.NewTicker(job.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			slog.Info("cron job triggered", "job", job.ID)
			s.callback(ctx, job)
		case <-ctx.Done():
			return
		}
	}
}

// parseSchedule 解析调度表达式。
// 目前支持 "every Nm" 和 "every Nh" 格式。
func parseSchedule(schedule string) (time.Duration, error) {
	schedule = strings.TrimSpace(strings.ToLower(schedule))

	if strings.HasPrefix(schedule, "every ") {
		expr := strings.TrimPrefix(schedule, "every ")
		expr = strings.TrimSpace(expr)
		if strings.HasSuffix(expr, "m") {
			n, err := strconv.Atoi(strings.TrimSuffix(expr, "m"))
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("invalid minute interval: %q", expr)
			}
			return time.Duration(n) * time.Minute, nil
		}
		if strings.HasSuffix(expr, "h") {
			n, err := strconv.Atoi(strings.TrimSuffix(expr, "h"))
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("invalid hour interval: %q", expr)
			}
			return time.Duration(n) * time.Hour, nil
		}
		if strings.HasSuffix(expr, "s") {
			n, err := strconv.Atoi(strings.TrimSuffix(expr, "s"))
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("invalid second interval: %q", expr)
			}
			return time.Duration(n) * time.Second, nil
		}
	}

	return 0, fmt.Errorf("unsupported schedule format (use 'every Nm', 'every Nh', or 'every Ns')")
}
