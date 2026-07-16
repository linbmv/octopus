package helper

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/bestruirui/octopus/internal/client"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/xstrings"
)

func ChannelHttpClient(channel *model.Channel) (*http.Client, error) {
	if channel == nil {
		return nil, errors.New("channel is nil")
	}
	if !channel.Proxy {
		return client.GetHTTPClientSystemProxy(false)
	} else if channel.ChannelProxy == nil || strings.TrimSpace(*channel.ChannelProxy) == "" {
		return client.GetHTTPClientSystemProxy(true)
	} else {
		return client.GetHTTPClientCustomProxy(strings.TrimSpace(*channel.ChannelProxy))
	}
}

func ChannelBaseUrlDelayUpdate(channel *model.Channel, ctx context.Context) {
	if channel == nil {
		return
	}
	newBaseUrls := make([]model.BaseUrl, 0, len(channel.BaseUrls))
	for _, baseUrl := range channel.BaseUrls {
		if baseUrl.URL == "" {
			continue
		}
		httpClient, err := ChannelHttpClient(channel)
		if err != nil {
			log.Warnf("failed to get http client (channel=%d): %v", channel.ID, err)
			continue
		}
		delay, err := GetUrlDelay(httpClient, baseUrl.URL, ctx)
		if err != nil {
			log.Warnf("failed to get url delay (channel=%d): %v", channel.ID, err)
			continue
		}
		newBaseUrls = append(newBaseUrls, model.BaseUrl{
			URL:   baseUrl.URL,
			Delay: delay,
		})
	}
	if len(newBaseUrls) > 0 {
		if err := op.ChannelBaseUrlUpdate(channel.ID, newBaseUrls); err != nil {
			log.Warnf("failed to update base URL delay cache (channel=%d): %v", channel.ID, err)
		}
	}
}

func ChannelAutoGroup(channel *model.Channel, ctx context.Context) {
	if channel == nil {
		return
	}
	channelModelNames := xstrings.SplitTrimCompact(",", channel.Model, channel.CustomModel)
	if err := op.GroupItemPruneByChannelModels(channel.ID, channelModelNames, ctx); err != nil {
		log.Warnf("prune stale group items failed (channel=%d): %v", channel.ID, err)
	}
	if channel.AutoGroup == model.AutoGroupTypeNone || len(channelModelNames) == 0 {
		return
	}
	groups, err := op.GroupList(ctx)
	if err != nil {
		log.Warnf("get group list failed: %v", err)
		return
	}

	for _, group := range groups {
		matchedModelNames := matchModelsToGroup(channel, group, channelModelNames)
		if len(matchedModelNames) > 0 {
			addMatchedModelsToGroup(channel.ID, group.ID, matchedModelNames, ctx)
		}
	}
}

func matchModelsToGroup(channel *model.Channel, group model.Group, channelModelNames []string) []string {
	switch channel.AutoGroup {
	case model.AutoGroupTypeExact:
		return matchExact(group.Name, channelModelNames)
	case model.AutoGroupTypeFuzzy:
		return matchFuzzy(group.Name, channelModelNames)
	case model.AutoGroupTypeRegex:
		return matchRegex(channel.ID, group, channelModelNames)
	default:
		return nil
	}
}

func matchExact(groupName string, modelNames []string) []string {
	matched := make([]string, 0, len(modelNames))
	for _, modelName := range modelNames {
		if strings.EqualFold(modelName, groupName) {
			matched = append(matched, modelName)
		}
	}
	return matched
}

func matchFuzzy(groupName string, modelNames []string) []string {
	groupNameLower := strings.ToLower(strings.TrimSpace(groupName))
	if groupNameLower == "" {
		return nil
	}
	matched := make([]string, 0, len(modelNames))
	for _, modelName := range modelNames {
		if strings.Contains(strings.ToLower(modelName), groupNameLower) {
			matched = append(matched, modelName)
		}
	}
	return matched
}

func matchRegex(channelID int, group model.Group, modelNames []string) []string {
	if group.MatchRegex == "" {
		return matchExact(group.Name, modelNames)
	}
	re, err := CompileModelRegex(group.MatchRegex)
	if err != nil {
		log.Warnf("compile regex failed (channel=%d group=%d): %v", channelID, group.ID, err)
		return nil
	}
	matched := make([]string, 0, len(modelNames))
	for _, modelName := range modelNames {
		isMatch, err := MatchModelRegex(re, modelName)
		if err != nil {
			log.Warnf("match regex failed (channel=%d group=%d): %v", channelID, group.ID, err)
			continue
		}
		if isMatch {
			matched = append(matched, modelName)
		}
	}
	return matched
}

func addMatchedModelsToGroup(channelID, groupID int, modelNames []string, ctx context.Context) {
	items := make([]model.GroupIDAndLLMName, 0, len(modelNames))
	for _, modelName := range modelNames {
		items = append(items, model.GroupIDAndLLMName{
			ChannelID: channelID,
			ModelName: modelName,
		})
	}
	if err := op.GroupItemBatchAdd(groupID, items, ctx); err != nil {
		log.Warnf("group item batch add failed (channel=%d group=%d): %v", channelID, groupID, err)
	}
}
