package task

import (
	"context"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/log"
)

func ChannelBaseUrlDelayTaskContext(parent context.Context) error {
	log.Debugf("channel base url delay task started")
	startTime := time.Now()
	defer func() {
		log.Debugf("channel base url delay task finished, update time: %s", time.Since(startTime))
	}()
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()
	channels, err := op.ChannelList(ctx)
	if err != nil {
		return err
	}
	for _, channel := range channels {
		if err := ctx.Err(); err != nil {
			return err
		}
		helper.ChannelBaseUrlDelayUpdate(&channel, ctx)
	}
	return ctx.Err()
}
