// Package cron 实现简易定时任务调度。
//
// 支持两种调度格式：
//   - "every Nm/Nh/Ns" — 固定间隔（如 every 5m）
//   - "daily HH:MM"    — 每天固定时间点（如 daily 19:00）
//
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

// scheduleType 标记调度类型。
type scheduleType int

const (
	schedInterval scheduleType = iota // 固定间隔
	schedDaily                        // 每日定时
)

// Job 描述一个定时任务。
type Job struct {
	ID       string
	Schedule string // "every 5m", "daily 19:00" 等
	Prompt   string // 触发时发送给 agent 的 prompt

	sType    scheduleType
	interval time.Duration // schedInterval 使用
	hour     int           // schedDaily 使用
	minute   int           // schedDaily 使用
}

// Callback 是定时任务触发时的回调。
type Callback func(ctx context.Context, job Job)

// Service 管理和调度定时任务。支持运行时动态添加/删除任务。
type Service struct {
	mu       sync.Mutex
	jobs     map[string]Job           // id -> job
	cancels  map[string]context.CancelFunc // id -> cancel function
	callback Callback
	rootCtx  context.Context
	rootCancel context.CancelFunc
	wg       sync.WaitGroup
	started  bool
}

// NewService 创建 cron 服务。
func NewService(callback Callback) *Service {
	return &Service{
		jobs:    make(map[string]Job),
		cancels: make(map[string]context.CancelFunc),
		callback: callback,
	}
}
// SetCallback 设置定时任务触发时的回调。
func (s *Service) SetCallback(cb Callback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callback = cb
}
// Add 添加一个定时任务。若服务已启动，任务会立即开始调度。
func (s *Service) Add(id, schedule, prompt string) error {
	job, err := parseSchedule(id, schedule, prompt)
	if err != nil {
		return fmt.Errorf("invalid schedule %q: %w", schedule, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 如果已存在同 ID 的任务，先取消旧的
	if cancel, ok := s.cancels[id]; ok {
		cancel()
	}

	s.jobs[id] = job

	// 如果服务已启动，立即开始调度新任务
	if s.started {
		s.startJobLocked(job)
	}

	return nil
}

// Remove 移除一个定时任务。
func (s *Service) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cancel, ok := s.cancels[id]; ok {
		cancel()
		delete(s.cancels, id)
	}
	if _, ok := s.jobs[id]; ok {
		delete(s.jobs, id)
		return true
	}
	return false
}

// List 返回所有已注册的任务列表。
func (s *Service) List() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j)
	}
	return out
}

// Start 启动所有定时任务。
func (s *Service) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	s.rootCtx = ctx
	s.rootCancel = cancel
	s.started = true

	for _, job := range s.jobs {
		s.startJobLocked(job)
	}
	slog.Info("cron service started", "jobs", len(s.jobs))
}

func (s *Service) startJobLocked(job Job) {
	jobCtx, jobCancel := context.WithCancel(s.rootCtx)
	s.cancels[job.ID] = jobCancel
	s.wg.Add(1)
	go s.runJob(jobCtx, job)
}

// Stop 停止所有定时任务。
func (s *Service) Stop() {
	s.mu.Lock()
	if s.rootCancel != nil {
		s.rootCancel()
	}
	s.started = false
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Service) runJob(ctx context.Context, job Job) {
	defer s.wg.Done()

	switch job.sType {
	case schedInterval:
		s.runIntervalJob(ctx, job)
	case schedDaily:
		s.runDailyJob(ctx, job)
	}
}

func (s *Service) runIntervalJob(ctx context.Context, job Job) {
	ticker := time.NewTicker(job.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			slog.Info("cron job triggered", "job", job.ID, "schedule", job.Schedule)
			s.callback(ctx, job)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) runDailyJob(ctx context.Context, job Job) {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), job.hour, job.minute, 0, 0, now.Location())
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		delay := next.Sub(now)

		slog.Debug("daily job scheduled", "job", job.ID, "next", next.Format("2006-01-02 15:04:05"), "delay", delay)

		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
			slog.Info("cron job triggered", "job", job.ID, "schedule", job.Schedule)
			s.callback(ctx, job)
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

// parseSchedule 解析调度表达式。
// 支持格式：
//   - "every Nm" / "every Nh" / "every Ns" — 固定间隔
//   - "daily HH:MM" — 每天定时
func parseSchedule(id, schedule, prompt string) (Job, error) {
	raw := schedule
	schedule = strings.TrimSpace(strings.ToLower(schedule))

	job := Job{ID: id, Schedule: raw, Prompt: prompt}

	// "daily HH:MM"
	if strings.HasPrefix(schedule, "daily ") {
		timeStr := strings.TrimSpace(strings.TrimPrefix(schedule, "daily "))
		parts := strings.Split(timeStr, ":")
		if len(parts) != 2 {
			return Job{}, fmt.Errorf("daily format requires HH:MM, got %q", timeStr)
		}
		h, err := strconv.Atoi(parts[0])
		if err != nil || h < 0 || h > 23 {
			return Job{}, fmt.Errorf("invalid hour: %q", parts[0])
		}
		m, err := strconv.Atoi(parts[1])
		if err != nil || m < 0 || m > 59 {
			return Job{}, fmt.Errorf("invalid minute: %q", parts[1])
		}
		job.sType = schedDaily
		job.hour = h
		job.minute = m
		return job, nil
	}

	// "every ..."
	if strings.HasPrefix(schedule, "every ") {
		expr := strings.TrimSpace(strings.TrimPrefix(schedule, "every "))
		var dur time.Duration
		var err error

		switch {
		case strings.HasSuffix(expr, "m"):
			n, e := strconv.Atoi(strings.TrimSuffix(expr, "m"))
			if e != nil || n <= 0 {
				return Job{}, fmt.Errorf("invalid minute interval: %q", expr)
			}
			dur = time.Duration(n) * time.Minute
		case strings.HasSuffix(expr, "h"):
			n, e := strconv.Atoi(strings.TrimSuffix(expr, "h"))
			if e != nil || n <= 0 {
				return Job{}, fmt.Errorf("invalid hour interval: %q", expr)
			}
			dur = time.Duration(n) * time.Hour
		case strings.HasSuffix(expr, "s"):
			n, e := strconv.Atoi(strings.TrimSuffix(expr, "s"))
			if e != nil || n <= 0 {
				return Job{}, fmt.Errorf("invalid second interval: %q", expr)
			}
			dur = time.Duration(n) * time.Second
		default:
			// 尝试 time.ParseDuration
			dur, err = time.ParseDuration(expr)
			if err != nil || dur <= 0 {
				return Job{}, fmt.Errorf("invalid interval: %q", expr)
			}
		}

		job.sType = schedInterval
		job.interval = dur
		return job, nil
	}

	return Job{}, fmt.Errorf("unsupported schedule format (use 'every Nm', 'every Nh', 'every Ns', or 'daily HH:MM')")
}
