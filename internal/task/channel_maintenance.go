package task

import (
	"context"
	"fmt"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	appruntime "github.com/bestruirui/octopus/internal/runtime"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/xstrings"
)

const channelMaintenanceQueueSize = 64

var channelMaintenanceRunner = runChannelMaintenance

var channelMaintenanceQueue = mustChannelMaintenanceQueue()

func mustChannelMaintenanceQueue() *appruntime.JobQueue[model.Channel] {
	queue, err := appruntime.NewJobQueue(appruntime.JobQueueConfig[model.Channel]{
		Name:        "channel_maintenance",
		QueueDepth:  channelMaintenanceQueueSize,
		Concurrency: 1,
		Key: func(channel model.Channel) string {
			if channel.ID == 0 {
				return ""
			}
			return fmt.Sprintf("%d", channel.ID)
		},
		Handle: func(ctx context.Context, channel model.Channel) error {
			return channelMaintenanceRunner(ctx, channel)
		},
		OnError: func(err error) {
			log.Warnf("channel maintenance job failed: %v", err)
		},
	})
	if err != nil {
		panic(err)
	}
	return queue
}

func startChannelMaintenance(ctx context.Context) error {
	return channelMaintenanceQueue.Start(ctx)
}

func SubmitChannelMaintenance(channel model.Channel) bool {
	if channel.ID == 0 {
		log.Warnf("channel maintenance skipped: empty channel id")
		return false
	}

	result := channelMaintenanceQueue.Submit(cloneChannelForMaintenance(channel))
	switch result {
	case appruntime.SubmitAccepted:
		return true
	case appruntime.SubmitCoalesced:
		log.Debugf("channel maintenance already pending: channel=%d", channel.ID)
		return true
	case appruntime.SubmitDropped:
		log.Warnf("channel maintenance queue full, skipping channel=%d", channel.ID)
	default:
		log.Warnf("channel maintenance worker is stopped, skipping channel=%d", channel.ID)
	}
	return false
}

func stopChannelMaintenance(ctx context.Context) error {
	return channelMaintenanceQueue.Stop(ctx)
}

func ChannelMaintenanceQueueStats() appruntime.QueueStats {
	return channelMaintenanceQueue.Stats()
}

func runChannelMaintenance(parent context.Context, channel model.Channel) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()

	modelNames := xstrings.SplitTrimCompact(",", channel.Model, channel.CustomModel)
	var result error
	if err := helper.LLMPriceAddToDB(modelNames, ctx); err != nil {
		result = fmt.Errorf("channel %d price update: %w", channel.ID, err)
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	helper.ChannelBaseUrlDelayUpdate(&channel, ctx)
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	helper.ChannelAutoGroup(&channel, ctx)
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return result
}

func cloneChannelForMaintenance(channel model.Channel) model.Channel {
	if channel.BaseUrls != nil {
		channel.BaseUrls = append([]model.BaseUrl(nil), channel.BaseUrls...)
	}
	if channel.Keys != nil {
		channel.Keys = append([]model.ChannelKey(nil), channel.Keys...)
	}
	if channel.CustomHeader != nil {
		channel.CustomHeader = append([]model.CustomHeader(nil), channel.CustomHeader...)
	}
	if channel.HeaderRules != nil {
		channel.HeaderRules = append([]model.HeaderRule(nil), channel.HeaderRules...)
	}
	if channel.JSONRewriteRules != nil {
		channel.JSONRewriteRules = append([]model.JSONRewriteRule(nil), channel.JSONRewriteRules...)
		for i := range channel.JSONRewriteRules {
			if channel.JSONRewriteRules[i].Value != nil {
				value := *channel.JSONRewriteRules[i].Value
				channel.JSONRewriteRules[i].Value = &value
			}
		}
	}
	channel.Stats = nil
	return channel
}
