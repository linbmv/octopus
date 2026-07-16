package op

import (
	"context"
	"errors"
	"time"
)

// AsyncWorker groups persistence workers behind the application runtime
// lifecycle. The service-level methods remain available for focused tests.
type AsyncWorker struct {
	logs  *RelayLogService
	stats *StatsService
}

func NewAsyncWorker(logs *RelayLogService, stats *StatsService) *AsyncWorker {
	if logs == nil {
		logs = relayLogService
	}
	if stats == nil {
		stats = statsService
	}
	return &AsyncWorker{logs: logs, stats: stats}
}

var defaultAsyncWorker = NewAsyncWorker(relayLogService, statsService)

func DefaultAsyncWorker() *AsyncWorker { return defaultAsyncWorker }

func (w *AsyncWorker) Start(ctx context.Context) error {
	if err := w.logs.StartFlushWorkerContext(ctx); err != nil {
		return err
	}
	if err := w.stats.StartSaveWorkerContext(ctx); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return errors.Join(err, w.logs.StopFlushWorker(stopCtx))
	}
	return nil
}

func (w *AsyncWorker) Stop(ctx context.Context) error {
	var result error
	if err := w.stats.StopSaveWorker(ctx); err != nil && !errors.Is(err, context.Canceled) {
		result = errors.Join(result, err)
	}
	if err := w.logs.StopFlushWorker(ctx); err != nil && !errors.Is(err, context.Canceled) {
		result = errors.Join(result, err)
	}
	return result
}

func (w *AsyncWorker) Flush(ctx context.Context) error {
	if w == defaultAsyncWorker {
		return SaveCacheContext(ctx)
	}
	var result error
	if err := w.stats.SaveDB(ctx); err != nil {
		result = errors.Join(result, err)
	}
	if err := w.logs.SaveDBTask(ctx); err != nil {
		result = errors.Join(result, err)
	}
	return result
}

func StartAsyncWorkers() {
	_ = defaultAsyncWorker.Start(context.Background())
}

func StopAsyncWorkers() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return defaultAsyncWorker.Stop(ctx)
}
