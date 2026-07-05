package task

import (
	"context"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/xstrings"
)

const channelMaintenanceQueueSize = 64

var (
	channelMaintenanceOnce    sync.Once
	channelMaintenanceQueue   chan model.Channel
	channelMaintenancePending = make(map[int]struct{})
	channelMaintenanceMu      sync.Mutex
	channelMaintenanceRunner  = runChannelMaintenance
)

func SubmitChannelMaintenance(channel model.Channel) bool {
	if channel.ID == 0 {
		log.Warnf("channel maintenance skipped: empty channel id")
		return false
	}

	channelMaintenanceOnce.Do(func() {
		channelMaintenanceQueue = make(chan model.Channel, channelMaintenanceQueueSize)
		go channelMaintenanceWorker()
	})

	channelMaintenanceMu.Lock()
	if _, exists := channelMaintenancePending[channel.ID]; exists {
		channelMaintenanceMu.Unlock()
		log.Debugf("channel maintenance already pending: channel=%d", channel.ID)
		return true
	}
	channelMaintenancePending[channel.ID] = struct{}{}
	channelMaintenanceMu.Unlock()

	select {
	case channelMaintenanceQueue <- cloneChannelForMaintenance(channel):
		return true
	default:
		channelMaintenanceMu.Lock()
		delete(channelMaintenancePending, channel.ID)
		channelMaintenanceMu.Unlock()
		log.Warnf("channel maintenance queue full, skipping channel=%d", channel.ID)
		return false
	}
}

func channelMaintenanceWorker() {
	for channel := range channelMaintenanceQueue {
		channelMaintenanceRunner(channel)
		channelMaintenanceMu.Lock()
		delete(channelMaintenancePending, channel.ID)
		channelMaintenanceMu.Unlock()
	}
}

func runChannelMaintenance(channel model.Channel) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	modelNames := xstrings.SplitTrimCompact(",", channel.Model, channel.CustomModel)
	if err := helper.LLMPriceAddToDB(modelNames, ctx); err != nil {
		log.Warnf("channel maintenance price update failed (channel=%d): %v", channel.ID, err)
	}
	helper.ChannelBaseUrlDelayUpdate(&channel, ctx)
	helper.ChannelAutoGroup(&channel, ctx)
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
	channel.Stats = nil
	return channel
}
