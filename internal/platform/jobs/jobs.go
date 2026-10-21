// Package jobs wraps asynq: a Client for enqueueing (shared by every module) and a
// Worker that runs handlers and periodic sweeps. Both reuse the app's Redis pool.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// Enqueuer is what modules depend on to schedule background work.
type Enqueuer interface {
	Enqueue(ctx context.Context, task *asynq.Task, opts ...asynq.Option) error
}

type Client struct {
	c *asynq.Client
}

func NewClient(rdb redis.UniversalClient) *Client {
	return &Client{c: asynq.NewClientFromRedisClient(rdb)}
}

// Enqueue schedules a task. A task with the same TaskID already queued counts as
// success, which makes "schedule X" calls idempotent.
func (c *Client) Enqueue(ctx context.Context, task *asynq.Task, opts ...asynq.Option) error {
	_, err := c.c.EnqueueContext(ctx, task, opts...)
	if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", task.Type(), err)
	}
	return nil
}

// Close releases the client; the shared Redis pool stays open.
func (c *Client) Close() error { return c.c.Close() }

// Worker processes tasks and fires periodic (cron) tasks.
type Worker struct {
	srv   *asynq.Server
	mux   *asynq.ServeMux
	sched *asynq.Scheduler
	log   *slog.Logger
}

func NewWorker(rdb redis.UniversalClient, concurrency int, log *slog.Logger) *Worker {
	logger := asynqLogger{log.With("component", "jobs")}
	return &Worker{
		srv: asynq.NewServerFromRedisClient(rdb, asynq.Config{
			Concurrency: concurrency,
			Logger:      logger,
			LogLevel:    asynq.WarnLevel,
			ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, t *asynq.Task, err error) {
				log.ErrorContext(ctx, "job failed", "type", t.Type(), "err", err)
			}),
		}),
		mux:   asynq.NewServeMux(),
		sched: asynq.NewSchedulerFromRedisClient(rdb, &asynq.SchedulerOpts{Logger: logger, LogLevel: asynq.WarnLevel}),
		log:   log,
	}
}

// Handle registers the handler for a task type.
func (w *Worker) Handle(taskType string, h func(context.Context, *asynq.Task) error) {
	w.mux.HandleFunc(taskType, h)
}

// Every enqueues task on a cron spec (e.g. "@every 1m"). Asynq's scheduler is
// leader-free, so with several instances each enqueues; give periodic tasks a
// Unique option or make their handlers idempotent.
func (w *Worker) Every(spec string, task *asynq.Task, opts ...asynq.Option) error {
	if _, err := w.sched.Register(spec, task, opts...); err != nil {
		return fmt.Errorf("register periodic %s: %w", task.Type(), err)
	}
	return nil
}

func (w *Worker) Start() error {
	if err := w.srv.Start(w.mux); err != nil {
		return fmt.Errorf("start job server: %w", err)
	}
	if err := w.sched.Start(); err != nil {
		w.srv.Shutdown()
		return fmt.Errorf("start scheduler: %w", err)
	}
	return nil
}

// Shutdown stops fetching new work and waits for in-flight handlers.
func (w *Worker) Shutdown() {
	w.sched.Shutdown()
	w.srv.Shutdown()
}

// asynqLogger adapts slog to asynq's printf-less Logger interface.
type asynqLogger struct{ l *slog.Logger }

func (a asynqLogger) Debug(args ...any) { a.l.Debug(fmt.Sprint(args...)) }
func (a asynqLogger) Info(args ...any)  { a.l.Info(fmt.Sprint(args...)) }
func (a asynqLogger) Warn(args ...any)  { a.l.Warn(fmt.Sprint(args...)) }
func (a asynqLogger) Error(args ...any) { a.l.Error(fmt.Sprint(args...)) }
func (a asynqLogger) Fatal(args ...any) { a.l.Error(fmt.Sprint(args...)); panic(fmt.Sprint(args...)) }
