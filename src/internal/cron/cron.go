// Package cron 实现定时任务调度，底层使用 robfig/cron/v3。
//
// 支持调度格式：
//   - "every Nm/Nh/Ns"  — 固定间隔（如 every 5m、every 1h）
//   - "daily HH:MM"     — 每天固定时间点（如 daily 19:00）
//   - 标准 5 字段 cron 表达式（如 "0 19 * * *"）
//
// 特性：
//   - 运行时动态添加/删除任务
//   - 执行历史日志（RunLog）
//   - 错误通知回调（OnError）
package cron

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Job 描述一个定时任务。
type Job struct {
	ID       string
	Schedule string // 原始调度表达式
	Prompt   string // 触发时发送给 agent 的 prompt
}

// RunRecord 记录一次任务执行历史。
type RunRecord struct {
	JobID     string        `json:"job_id"`
	StartTime time.Time     `json:"start_time"`
	EndTime   time.Time     `json:"end_time"`
	Duration  time.Duration `json:"duration"`
	Error     string        `json:"error,omitempty"`
	Success   bool          `json:"success"`
}

// Callback 是定时任务触发时的回调。
type Callback func(ctx context.Context, job Job) error

// ErrorNotifier 是任务执行失败时的通知回调。
type ErrorNotifier func(job Job, err error)

// Service 管理和调度定时任务，支持运行时动态添加/删除。
type Service struct {
	c        *cron.Cron
	callback Callback
	onError  ErrorNotifier
	entries  map[string]cron.EntryID // job ID -> cron entry ID
	jobs     map[string]Job

	// 执行历史日志
	logMu   sync.Mutex
	runLog  []RunRecord
	maxLogs int // 最大保留日志条数
}

// NewService 创建 cron 服务。
func NewService(callback Callback) *Service {
	return &Service{
		c:        cron.New(),
		callback: callback,
		entries:  make(map[string]cron.EntryID),
		jobs:     make(map[string]Job),
		maxLogs:  500,
	}
}

// SetCallback 设置定时任务触发时的回调。
func (s *Service) SetCallback(cb Callback) {
	s.callback = cb
}

// SetErrorNotifier 设置任务执行失败时的通知回调。
func (s *Service) SetErrorNotifier(fn ErrorNotifier) {
	s.onError = fn
}

// Add 添加一个定时任务。若服务已启动，任务会立即开始调度。
func (s *Service) Add(id, schedule, prompt string) error {
	spec, err := toSpec(schedule)
	if err != nil {
		return fmt.Errorf("invalid schedule %q: %w", schedule, err)
	}

	// 如果已存在同 ID 的任务，先移除
	if entryID, ok := s.entries[id]; ok {
		s.c.Remove(entryID)
	}

	job := Job{ID: id, Schedule: schedule, Prompt: prompt}
	entryID, err := s.c.AddFunc(spec, func() {
		slog.Info("cron job triggered", "job", id, "schedule", schedule)
		s.executeJob(job)
	})
	if err != nil {
		return fmt.Errorf("add cron func: %w", err)
	}

	s.entries[id] = entryID
	s.jobs[id] = job
	return nil
}

// executeJob 执行一个 cron 任务并记录日志。
func (s *Service) executeJob(job Job) {
	start := time.Now()
	var jobErr error

	if s.callback != nil {
		jobErr = s.callback(context.Background(), job)
	}

	record := RunRecord{
		JobID:     job.ID,
		StartTime: start,
		EndTime:   time.Now(),
		Duration:  time.Since(start),
		Success:   jobErr == nil,
	}
	if jobErr != nil {
		record.Error = jobErr.Error()
		slog.Error("cron job failed", "job", job.ID, "err", jobErr, "duration", record.Duration)

		// 错误通知
		if s.onError != nil {
			s.onError(job, jobErr)
		}
	} else {
		slog.Info("cron job completed", "job", job.ID, "duration", record.Duration)
	}

	s.appendLog(record)
}

// appendLog 追加执行日志，超出上限时丢弃最旧的。
func (s *Service) appendLog(record RunRecord) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.runLog = append(s.runLog, record)
	if len(s.runLog) > s.maxLogs {
		// 丢弃前 1/4
		drop := s.maxLogs / 4
		s.runLog = s.runLog[drop:]
	}
}

// RunHistory 返回执行历史（最近的在后）。可选指定 jobID 过滤。
func (s *Service) RunHistory(jobID string, limit int) []RunRecord {
	s.logMu.Lock()
	defer s.logMu.Unlock()

	if limit <= 0 {
		limit = 50
	}

	var results []RunRecord
	// 从后往前遍历
	for i := len(s.runLog) - 1; i >= 0 && len(results) < limit; i-- {
		r := s.runLog[i]
		if jobID == "" || r.JobID == jobID {
			results = append(results, r)
		}
	}
	return results
}

// Remove 移除一个定时任务。
func (s *Service) Remove(id string) bool {
	entryID, ok := s.entries[id]
	if !ok {
		return false
	}
	s.c.Remove(entryID)
	delete(s.entries, id)
	delete(s.jobs, id)
	return true
}

// List 返回所有已注册的任务列表。
func (s *Service) List() []Job {
	out := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j)
	}
	return out
}

// Start 启动调度器。
func (s *Service) Start() {
	s.c.Start()
	slog.Info("cron service started", "jobs", len(s.jobs))
}

// Stop 停止调度器，等待正在执行的任务完成。
func (s *Service) Stop() {
	ctx := s.c.Stop()
	<-ctx.Done()
}

// toSpec 将人类友好的调度表达式转换为 robfig/cron 认可的格式。
//
//	"every 5m"   → "@every 5m"
//	"daily 19:00"→ "0 19 * * *"
//	其他格式直接透传（标准 cron 表达式）
func toSpec(schedule string) (string, error) {
	schedule = strings.TrimSpace(schedule)

	// "every ..." 转换为 @every
	if strings.HasPrefix(schedule, "every ") {
		expr := strings.TrimSpace(strings.TrimPrefix(schedule, "every "))
		dur, err := parseEvery(expr)
		if err != nil {
			return "", err
		}
		_ = dur // 仅用于校验
		return "@every " + expr, nil
	}

	// "daily HH:MM" 转换为 cron 表达式
	if strings.HasPrefix(schedule, "daily ") {
		timeStr := strings.TrimSpace(strings.TrimPrefix(schedule, "daily "))
		h, m, err := parseTime(timeStr)
		if err != nil {
			return "", fmt.Errorf("invalid daily time %q: %w", timeStr, err)
		}
		return fmt.Sprintf("%d %d * * *", m, h), nil
	}

	// 透传（标准 cron 表达式 / @hourly / @daily 等）
	return schedule, nil
}

// parseEvery 校验 "Nm" / "Nh" / "Ns" 或 time.ParseDuration 支持的格式。
func parseEvery(expr string) (time.Duration, error) {
	// 先尝试 Go duration 语法 (5m, 1h, 30s, 1h30m ...)
	dur, err := time.ParseDuration(expr)
	if err == nil {
		if dur <= 0 {
			return 0, fmt.Errorf("duration must be positive")
		}
		return dur, nil
	}

	// 兼容无单位数字前原有的 "5m" 写法（time.ParseDuration 已支持，此处冗余但保留可读性）
	for _, suffix := range []string{"m", "h", "s"} {
		if strings.HasSuffix(expr, suffix) {
			n, e := strconv.Atoi(strings.TrimSuffix(expr, suffix))
			if e == nil && n > 0 {
				return 0, nil // 格式合法，robfig 会自行解析
			}
		}
	}
	return 0, fmt.Errorf("invalid interval %q (examples: 5m, 1h, 30s)", expr)
}

// parseTime 解析 "HH:MM" 格式，返回 hour 和 minute。
func parseTime(s string) (hour, minute int, err error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected HH:MM")
	}
	h, e1 := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("invalid time value")
	}
	return h, m, nil
}
