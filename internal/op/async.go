package op

import (
	"context"
	"errors"
	"time"
)

func StartAsyncWorkers() {
	startRelayLogFlushWorker()
	startStatsSaveWorker()
}

func StopAsyncWorkers() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var err error
	if stopErr := stopRelayLogFlushWorker(ctx); stopErr != nil && !errors.Is(stopErr, context.Canceled) {
		err = errors.Join(err, stopErr)
	}
	if stopErr := stopStatsSaveWorker(ctx); stopErr != nil && !errors.Is(stopErr, context.Canceled) {
		err = errors.Join(err, stopErr)
	}
	return err
}
