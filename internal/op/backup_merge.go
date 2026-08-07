package op

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxDBImportIssues = 256

var ErrDBImportConflict = errors.New("database import conflicts with target data")

// DBImportConflictError carries the bounded dry-run style report for an actual
// import that was rejected before commit.
type DBImportConflictError struct {
	Result *model.DBImportResult
}

func (e *DBImportConflictError) Error() string {
	if e == nil || e.Result == nil {
		return ErrDBImportConflict.Error()
	}
	return fmt.Sprintf("%s: %d conflicts, %d unresolved references",
		ErrDBImportConflict, importResultCount(e.Result, func(s model.DBImportTableSummary) int64 { return s.Conflict }),
		importResultCount(e.Result, func(s model.DBImportTableSummary) int64 { return s.Unresolved }))
}

func (e *DBImportConflictError) Unwrap() error { return ErrDBImportConflict }

// DBImportV2 incrementally imports a version 2 dump by stable UUID. Numeric IDs
// from the source are used only to remap optional statistics and historical log
// references; they are never inserted into an existing target.
func DBImportV2(ctx context.Context, dump *model.DBDump, options model.DBImportOptions) (*model.DBImportResult, error) {
	if err := validateDBDump(dump); err != nil {
		return nil, err
	}
	if dump.Version != dbDumpVersion {
		return nil, invalidDump("version", "incremental import requires version %d", dbDumpVersion)
	}
	policy, err := normalizeDBImportPolicy(options.ConflictPolicy)
	if err != nil {
		return nil, err
	}

	result := &model.DBImportResult{
		Mode:           model.DBImportModeIncremental,
		DryRun:         options.DryRun,
		ConflictPolicy: policy,
		Tables:         make(map[string]model.DBImportTableSummary),
		RowsAffected:   make(map[string]int64),
	}

	dbRestoreMu.Lock()
	defer dbRestoreMu.Unlock()

	conn := db.GetDB()
	if conn == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	err = conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		executor := &dbImportV2Executor{
			tx:                 tx,
			dryRun:             options.DryRun,
			policy:             policy,
			result:             result,
			nextVirtualID:      -1,
			channelIDByOld:     make(map[int]int, len(dump.Channels)),
			channelIDByUUID:    make(map[string]int, len(dump.Channels)),
			channelKeyIDByOld:  make(map[int]int, len(dump.ChannelKeys)),
			groupIDByOld:       make(map[int]int, len(dump.Groups)),
			groupIDByUUID:      make(map[string]int, len(dump.Groups)),
			groupNameByOldName: make(map[string]string, len(dump.Groups)),
			apiKeyIDByOld:      make(map[int]int, len(dump.APIKeys)),
		}
		if err := executor.run(dump); err != nil {
			return err
		}
		sortDBImportIssues(result)
		if executor.hasBlockingIssues() && !executor.dryRun {
			return &DBImportConflictError{Result: result}
		}
		if !executor.dryRun && tx.Migrator().HasTable(&model.CapabilityEvidence{}) {
			// Incremental imports can rewrite channels, keys, models and endpoint
			// relationships outside ChannelUpdate. Runtime evidence is cheap to
			// regenerate, so invalidate it atomically with the import.
			if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.CapabilityEvidence{}).Error; err != nil {
				return fmt.Errorf("clear capability evidence after import: %w", err)
			}
			bumpCapabilityEvidenceVersion()
		}
		return nil
	})
	if err != nil {
		var conflictErr *DBImportConflictError
		if errors.As(err, &conflictErr) {
			return result, conflictErr
		}
		return nil, err
	}
	return result, nil
}

func normalizeDBImportPolicy(policy model.DBImportConflictPolicy) (model.DBImportConflictPolicy, error) {
	if policy == "" {
		return model.DBImportConflictReject, nil
	}
	switch policy {
	case model.DBImportConflictReject, model.DBImportConflictSkip, model.DBImportConflictReplace, model.DBImportConflictMerge:
		return policy, nil
	default:
		return "", invalidDump("conflict_policy", "must be one of reject, skip, replace, or merge")
	}
}

type dbImportV2Executor struct {
	tx     *gorm.DB
	dryRun bool
	policy model.DBImportConflictPolicy
	result *model.DBImportResult

	nextVirtualID int

	channelIDByOld     map[int]int
	channelIDByUUID    map[string]int
	channelKeyIDByOld  map[int]int
	groupIDByOld       map[int]int
	groupIDByUUID      map[string]int
	groupNameByOldName map[string]string
	apiKeyIDByOld      map[int]int
}

func (e *dbImportV2Executor) run(dump *model.DBDump) error {
	if err := e.importChannels(dump.Channels); err != nil {
		return err
	}
	if err := e.importGroups(dump.Groups); err != nil {
		return err
	}
	if err := e.importAPIKeys(dump.APIKeys); err != nil {
		return err
	}
	if err := e.importChannelKeys(dump); err != nil {
		return err
	}
	if err := e.importGroupItems(dump); err != nil {
		return err
	}
	return e.importNonIdentityTables(dump)
}

func (e *dbImportV2Executor) importChannels(rows []model.Channel) error {
	var existing []model.Channel
	if err := e.tx.Find(&existing).Error; err != nil {
		return fmt.Errorf("read target channels: %w", err)
	}
	byUUID := make(map[string]model.Channel, len(existing))
	byName := make(map[string]model.Channel, len(existing))
	for _, row := range existing {
		byUUID[canonicalUUID(row.UUID)] = row
		byName[strings.ToLower(row.Name)] = row
	}

	for _, source := range rows {
		oldID, sourceUUID := source.ID, canonicalUUID(source.UUID)
		source.UUID = sourceUUID
		uuidTarget, uuidMatch := byUUID[sourceUUID]
		nameTarget, nameMatch := byName[strings.ToLower(source.Name)]
		if uuidMatch && nameMatch && uuidTarget.ID != nameTarget.ID {
			e.unresolved("channels", sourceUUID, "name", "UUID and name match different target channels")
			e.channelIDByOld[oldID] = uuidTarget.ID
			e.channelIDByUUID[sourceUUID] = uuidTarget.ID
			continue
		}
		target, matched := uuidTarget, uuidMatch
		if !matched {
			target, matched = nameTarget, nameMatch
		}
		if matched {
			e.conflict("channels", sourceUUID, "uuid", "target channel already exists")
			if e.policy == model.DBImportConflictReject || e.policy == model.DBImportConflictSkip {
				e.skip("channels")
				e.channelIDByOld[oldID] = target.ID
				e.channelIDByUUID[sourceUUID] = target.ID
				continue
			}
			source.ID = target.ID
			if e.policy == model.DBImportConflictMerge && target.UUID != sourceUUID {
				source.UUID = target.UUID
			}
			if err := e.updateChannel(&source); err != nil {
				return err
			}
			e.updated("channels")
			e.channelIDByOld[oldID] = target.ID
			e.channelIDByUUID[sourceUUID] = target.ID
			continue
		}

		source.ID = 0
		if err := e.createChannel(&source); err != nil {
			return err
		}
		e.created("channels")
		e.channelIDByOld[oldID] = source.ID
		e.channelIDByUUID[sourceUUID] = source.ID
	}
	return nil
}

func (e *dbImportV2Executor) importGroups(rows []model.Group) error {
	var existing []model.Group
	if err := e.tx.Find(&existing).Error; err != nil {
		return fmt.Errorf("read target groups: %w", err)
	}
	byUUID := make(map[string]model.Group, len(existing))
	byName := make(map[string]model.Group, len(existing))
	for _, row := range existing {
		byUUID[canonicalUUID(row.UUID)] = row
		byName[strings.ToLower(row.Name)] = row
	}

	for _, source := range rows {
		oldID, oldName, sourceUUID := source.ID, source.Name, canonicalUUID(source.UUID)
		source.UUID = sourceUUID
		uuidTarget, uuidMatch := byUUID[sourceUUID]
		nameTarget, nameMatch := byName[strings.ToLower(source.Name)]
		if uuidMatch && nameMatch && uuidTarget.ID != nameTarget.ID {
			e.unresolved("groups", sourceUUID, "name", "UUID and name match different target groups")
			e.groupIDByOld[oldID] = uuidTarget.ID
			e.groupIDByUUID[sourceUUID] = uuidTarget.ID
			e.groupNameByOldName[oldName] = uuidTarget.Name
			continue
		}
		target, matched := uuidTarget, uuidMatch
		if !matched {
			target, matched = nameTarget, nameMatch
		}
		if matched {
			e.conflict("groups", sourceUUID, "uuid", "target group already exists")
			if e.policy == model.DBImportConflictReject || e.policy == model.DBImportConflictSkip {
				e.skip("groups")
				e.groupIDByOld[oldID] = target.ID
				e.groupIDByUUID[sourceUUID] = target.ID
				e.groupNameByOldName[oldName] = target.Name
				continue
			}
			source.ID = target.ID
			if e.policy == model.DBImportConflictMerge && target.UUID != sourceUUID {
				source.UUID = target.UUID
			}
			if err := e.updateGroup(&source); err != nil {
				return err
			}
			e.updated("groups")
			e.groupIDByOld[oldID] = target.ID
			e.groupIDByUUID[sourceUUID] = target.ID
			e.groupNameByOldName[oldName] = source.Name
			continue
		}

		source.ID = 0
		if err := e.createGroup(&source); err != nil {
			return err
		}
		e.created("groups")
		e.groupIDByOld[oldID] = source.ID
		e.groupIDByUUID[sourceUUID] = source.ID
		e.groupNameByOldName[oldName] = source.Name
	}
	return nil
}

func (e *dbImportV2Executor) importAPIKeys(rows []model.APIKey) error {
	var existing []model.APIKey
	if err := e.tx.Find(&existing).Error; err != nil {
		return fmt.Errorf("read target API keys: %w", err)
	}
	byUUID := make(map[string]model.APIKey, len(existing))
	byValue := make(map[string]model.APIKey, len(existing))
	for _, row := range existing {
		byUUID[canonicalUUID(row.UUID)] = row
		byValue[row.APIKey] = row
	}
	for _, source := range rows {
		oldID, sourceUUID := source.ID, canonicalUUID(source.UUID)
		source.UUID = sourceUUID
		source.SupportedModels = e.rewriteSupportedModels(source.SupportedModels)
		uuidTarget, uuidMatch := byUUID[sourceUUID]
		valueTarget, valueMatch := byValue[source.APIKey]
		if uuidMatch && valueMatch && uuidTarget.ID != valueTarget.ID {
			e.unresolved("api_keys", sourceUUID, "api_key", "UUID and key value match different target API keys")
			e.apiKeyIDByOld[oldID] = uuidTarget.ID
			continue
		}
		target, matched := uuidTarget, uuidMatch
		if !matched {
			target, matched = valueTarget, valueMatch
		}
		if matched {
			e.conflict("api_keys", sourceUUID, "uuid", "target API key already exists")
			if e.policy == model.DBImportConflictReject || e.policy == model.DBImportConflictSkip {
				e.skip("api_keys")
				e.apiKeyIDByOld[oldID] = target.ID
				continue
			}
			source.ID = target.ID
			if e.policy == model.DBImportConflictMerge && target.UUID != sourceUUID {
				source.UUID = target.UUID
			}
			if err := e.updateAPIKey(&source); err != nil {
				return err
			}
			e.updated("api_keys")
			e.apiKeyIDByOld[oldID] = target.ID
			continue
		}
		source.ID = 0
		if err := e.createAPIKey(&source); err != nil {
			return err
		}
		e.created("api_keys")
		e.apiKeyIDByOld[oldID] = source.ID
	}
	return nil
}

func (e *dbImportV2Executor) importChannelKeys(dump *model.DBDump) error {
	var existing []model.ChannelKey
	if err := e.tx.Find(&existing).Error; err != nil {
		return fmt.Errorf("read target channel keys: %w", err)
	}
	byUUID := make(map[string]model.ChannelKey, len(existing))
	byNaturalKey := make(map[string]model.ChannelKey, len(existing))
	for _, row := range existing {
		byUUID[canonicalUUID(row.UUID)] = row
		byNaturalKey[channelKeyNaturalKey(row.ChannelID, row.ChannelKey)] = row
	}
	retainedIDs := make(map[int]struct{}, len(dump.ChannelKeys))

	for _, source := range dump.ChannelKeys {
		oldID, rawUUID := source.ID, source.UUID
		sourceUUID := canonicalUUID(rawUUID)
		source.UUID = sourceUUID
		channelUUID := canonicalUUID(dump.Relations.ChannelKeys[rawUUID])
		targetChannelID, ok := e.channelIDByUUID[channelUUID]
		if !ok {
			e.unresolved("channel_keys", sourceUUID, "channel_id", "channel UUID %s was not mapped", channelUUID)
			continue
		}
		source.ChannelID = targetChannelID
		uuidTarget, uuidMatch := byUUID[sourceUUID]
		naturalTarget, naturalMatch := byNaturalKey[channelKeyNaturalKey(targetChannelID, source.ChannelKey)]
		if uuidMatch && naturalMatch && uuidTarget.ID != naturalTarget.ID {
			e.unresolved("channel_keys", sourceUUID, "channel_key", "UUID and channel key value match different target rows")
			e.channelKeyIDByOld[oldID] = uuidTarget.ID
			retainedIDs[uuidTarget.ID] = struct{}{}
			continue
		}
		target, matched := uuidTarget, uuidMatch
		if !matched {
			target, matched = naturalTarget, naturalMatch
		}
		if matched {
			e.conflict("channel_keys", sourceUUID, "uuid", "target channel key already exists")
			retainedIDs[target.ID] = struct{}{}
			e.channelKeyIDByOld[oldID] = target.ID
			if e.policy == model.DBImportConflictReject || e.policy == model.DBImportConflictSkip {
				e.skip("channel_keys")
				continue
			}
			source.ID = target.ID
			if e.policy == model.DBImportConflictMerge && target.UUID != sourceUUID {
				source.UUID = target.UUID
			}
			if err := e.updateChannelKey(&source); err != nil {
				return err
			}
			e.updated("channel_keys")
			continue
		}
		source.ID = 0
		if err := e.createChannelKey(&source); err != nil {
			return err
		}
		e.created("channel_keys")
		e.channelKeyIDByOld[oldID] = source.ID
		if source.ID > 0 {
			retainedIDs[source.ID] = struct{}{}
		}
	}

	if e.policy != model.DBImportConflictReplace {
		return nil
	}
	importedParents := make(map[int]struct{}, len(e.channelIDByOld))
	for _, id := range e.channelIDByOld {
		importedParents[id] = struct{}{}
	}
	for _, row := range existing {
		if _, imported := importedParents[row.ChannelID]; !imported {
			continue
		}
		if _, retained := retainedIDs[row.ID]; retained {
			continue
		}
		if err := e.deleteByID("channel_keys", &model.ChannelKey{}, row.ID); err != nil {
			return err
		}
		var statsCount int64
		if err := e.tx.Model(&model.StatsChannelKey{}).Where("channel_key_id = ?", row.ID).Count(&statsCount).Error; err != nil {
			return fmt.Errorf("count replaced channel key stats %d: %w", row.ID, err)
		}
		if e.dryRun {
			if statsCount > 0 {
				summary := e.result.Tables["stats_channel_key"]
				summary.Delete += statsCount
				e.result.Tables["stats_channel_key"] = summary
				e.result.RowsAffected["stats_channel_key"] += statsCount
			}
			continue
		}
		result := e.tx.Where("channel_key_id = ?", row.ID).Delete(&model.StatsChannelKey{})
		if result.Error != nil {
			return fmt.Errorf("delete replaced channel key stats %d: %w", row.ID, result.Error)
		}
		if result.RowsAffected > 0 {
			summary := e.result.Tables["stats_channel_key"]
			summary.Delete += result.RowsAffected
			e.result.Tables["stats_channel_key"] = summary
			e.result.RowsAffected["stats_channel_key"] += result.RowsAffected
		}
	}
	return nil
}

func (e *dbImportV2Executor) importGroupItems(dump *model.DBDump) error {
	var existing []model.GroupItem
	if err := e.tx.Find(&existing).Error; err != nil {
		return fmt.Errorf("read target group items: %w", err)
	}
	byUUID := make(map[string]model.GroupItem, len(existing))
	byNaturalKey := make(map[string]model.GroupItem, len(existing))
	prospective := make(map[int]model.GroupItem, len(existing)+len(dump.GroupItems))
	for _, row := range existing {
		byUUID[canonicalUUID(row.UUID)] = row
		byNaturalKey[groupItemNaturalKey(row)] = row
		prospective[row.ID] = row
	}
	retainedIDs := make(map[int]struct{}, len(dump.GroupItems))

	for _, source := range dump.GroupItems {
		rawUUID := source.UUID
		sourceUUID := canonicalUUID(rawUUID)
		source.UUID = sourceUUID
		relation := dump.Relations.GroupItems[rawUUID]
		groupID, ok := e.groupIDByUUID[canonicalUUID(relation.GroupUUID)]
		if !ok {
			e.unresolved("group_items", sourceUUID, "group_id", "group UUID %s was not mapped", relation.GroupUUID)
			continue
		}
		source.GroupID = groupID
		switch source.Type {
		case model.GroupItemTypeChannel:
			channelID, found := e.channelIDByUUID[canonicalUUID(relation.ChannelUUID)]
			if !found {
				e.unresolved("group_items", sourceUUID, "channel_id", "channel UUID %s was not mapped", relation.ChannelUUID)
				continue
			}
			source.ChannelID = channelID
			source.TargetGroupID = 0
		case model.GroupItemTypeGroup:
			targetGroupID, found := e.groupIDByUUID[canonicalUUID(relation.TargetGroupUUID)]
			if !found {
				e.unresolved("group_items", sourceUUID, "target_group_id", "group UUID %s was not mapped", relation.TargetGroupUUID)
				continue
			}
			source.ChannelID = 0
			source.TargetGroupID = targetGroupID
		}

		uuidTarget, uuidMatch := byUUID[sourceUUID]
		naturalTarget, naturalMatch := byNaturalKey[groupItemNaturalKey(source)]
		if uuidMatch && naturalMatch && uuidTarget.ID != naturalTarget.ID {
			e.unresolved("group_items", sourceUUID, "relation", "UUID and relationship match different target group items")
			retainedIDs[uuidTarget.ID] = struct{}{}
			continue
		}
		target, matched := uuidTarget, uuidMatch
		if !matched {
			target, matched = naturalTarget, naturalMatch
		}
		if matched {
			e.conflict("group_items", sourceUUID, "uuid", "target group item already exists")
			retainedIDs[target.ID] = struct{}{}
			if e.policy == model.DBImportConflictReject || e.policy == model.DBImportConflictSkip {
				e.skip("group_items")
				continue
			}
			source.ID = target.ID
			if e.policy == model.DBImportConflictMerge && target.UUID != sourceUUID {
				source.UUID = target.UUID
			}
			if err := e.updateGroupItem(&source); err != nil {
				return err
			}
			prospective[source.ID] = source
			e.updated("group_items")
			continue
		}
		source.ID = 0
		if err := e.createGroupItem(&source); err != nil {
			return err
		}
		prospective[source.ID] = source
		e.created("group_items")
		if source.ID > 0 {
			retainedIDs[source.ID] = struct{}{}
		}
	}

	if e.policy == model.DBImportConflictReplace {
		importedParents := make(map[int]struct{}, len(e.groupIDByOld))
		for _, id := range e.groupIDByOld {
			importedParents[id] = struct{}{}
		}
		for _, row := range existing {
			if _, imported := importedParents[row.GroupID]; !imported {
				continue
			}
			if _, retained := retainedIDs[row.ID]; retained {
				continue
			}
			if err := e.deleteByID("group_items", &model.GroupItem{}, row.ID); err != nil {
				return err
			}
			delete(prospective, row.ID)
		}
	}
	e.validateProspectiveGroupGraph(prospective)
	return nil
}

func (e *dbImportV2Executor) validateProspectiveGroupGraph(items map[int]model.GroupItem) {
	graph := make(groupGraph)
	nodes := make([]int, 0)
	seen := make(map[int]struct{})
	for _, item := range items {
		if item.Type != model.GroupItemTypeGroup || item.GroupID == 0 || item.TargetGroupID == 0 {
			continue
		}
		graph[item.GroupID] = append(graph[item.GroupID], item.TargetGroupID)
		if _, ok := seen[item.GroupID]; !ok {
			seen[item.GroupID] = struct{}{}
			nodes = append(nodes, item.GroupID)
		}
		if _, ok := seen[item.TargetGroupID]; !ok {
			seen[item.TargetGroupID] = struct{}{}
			nodes = append(nodes, item.TargetGroupID)
		}
	}
	sort.Ints(nodes)
	for _, node := range nodes {
		if detectCycleInGraph(graph, node) {
			e.unresolved("group_items", "", "graph", "merged group relationships contain a cycle")
			return
		}
	}
	if depth := graphMaxDepth(graph); depth > MaxGroupNestDepth {
		e.unresolved("group_items", "", "graph", "merged group nesting depth %d exceeds maximum %d", depth, MaxGroupNestDepth)
	}
}

func (e *dbImportV2Executor) createChannel(row *model.Channel) error {
	if e.dryRun {
		row.ID = e.virtualID()
		return nil
	}
	return e.tx.Omit(clause.Associations).Create(row).Error
}

func (e *dbImportV2Executor) updateChannel(row *model.Channel) error {
	if e.dryRun {
		return nil
	}
	return e.tx.Model(&model.Channel{}).Where("id = ?", row.ID).Select("*").Omit("id", clause.Associations).Updates(row).Error
}

func (e *dbImportV2Executor) createGroup(row *model.Group) error {
	if e.dryRun {
		row.ID = e.virtualID()
		return nil
	}
	return e.tx.Omit(clause.Associations).Create(row).Error
}

func (e *dbImportV2Executor) updateGroup(row *model.Group) error {
	if e.dryRun {
		return nil
	}
	return e.tx.Model(&model.Group{}).Where("id = ?", row.ID).Select("*").Omit("id", clause.Associations).Updates(row).Error
}

func (e *dbImportV2Executor) createAPIKey(row *model.APIKey) error {
	if e.dryRun {
		row.ID = e.virtualID()
		return nil
	}
	return e.tx.Create(row).Error
}

func (e *dbImportV2Executor) updateAPIKey(row *model.APIKey) error {
	if e.dryRun {
		return nil
	}
	return e.tx.Model(&model.APIKey{}).Where("id = ?", row.ID).Select("*").Omit("id").Updates(row).Error
}

func (e *dbImportV2Executor) createChannelKey(row *model.ChannelKey) error {
	if e.dryRun {
		row.ID = e.virtualID()
		return nil
	}
	return e.tx.Create(row).Error
}

func (e *dbImportV2Executor) updateChannelKey(row *model.ChannelKey) error {
	if e.dryRun {
		return nil
	}
	return e.tx.Model(&model.ChannelKey{}).Where("id = ?", row.ID).Select("*").Omit("id").Updates(row).Error
}

func (e *dbImportV2Executor) createGroupItem(row *model.GroupItem) error {
	if e.dryRun {
		row.ID = e.virtualID()
		return nil
	}
	return e.tx.Create(row).Error
}

func (e *dbImportV2Executor) updateGroupItem(row *model.GroupItem) error {
	if e.dryRun {
		return nil
	}
	return e.tx.Model(&model.GroupItem{}).Where("id = ?", row.ID).Select("*").Omit("id").Updates(row).Error
}

func (e *dbImportV2Executor) deleteByID(table string, value any, id int) error {
	e.deleted(table)
	if e.dryRun {
		return nil
	}
	if err := e.tx.Delete(value, id).Error; err != nil {
		return fmt.Errorf("delete replaced %s row %d: %w", table, id, err)
	}
	return nil
}

func (e *dbImportV2Executor) importNonIdentityTables(dump *model.DBDump) error {
	if err := importStableRows(e, "llm_infos", dump.LLMInfos,
		func(row *model.LLMInfo) bool { return true },
		func(row model.LLMInfo) string { return row.Name }, false, true); err != nil {
		return err
	}
	// Settings are expected to exist on an initialized target and retain their
	// historical upsert behavior independently of the UUID conflict policy.
	if err := importStableRows(e, "settings", dump.Settings,
		func(row *model.Setting) bool { return true },
		func(row model.Setting) string { return string(row.Key) }, true, true); err != nil {
		return err
	}
	if !dump.IncludeStats {
		return e.importRelayLogs(dump)
	}
	if err := importStableRows(e, "stats_total", dump.StatsTotal,
		func(row *model.StatsTotal) bool { return true },
		func(row model.StatsTotal) string { return fmt.Sprint(row.ID) }, false, true); err != nil {
		return err
	}
	if err := importStableRows(e, "stats_daily", dump.StatsDaily,
		func(row *model.StatsDaily) bool { return true },
		func(row model.StatsDaily) string { return row.Date }, false, true); err != nil {
		return err
	}
	if err := importStableRows(e, "stats_hourly", dump.StatsHourly,
		func(row *model.StatsHourly) bool { return true },
		func(row model.StatsHourly) string { return fmt.Sprint(row.Hour) }, false, true); err != nil {
		return err
	}
	if err := importStableRows(e, "stats_channel", dump.StatsChannel,
		func(row *model.StatsChannel) bool {
			targetID, ok := e.channelIDByOld[row.ChannelID]
			if !ok {
				e.unresolved("stats_channel", "", "channel_id", "source channel id %d was not mapped", row.ChannelID)
				return false
			}
			row.ChannelID = targetID
			return true
		},
		func(row model.StatsChannel) string { return fmt.Sprint(row.ChannelID) }, false, true); err != nil {
		return err
	}
	if err := importStableRows(e, "stats_channel_key", dump.StatsChannelKey,
		func(row *model.StatsChannelKey) bool {
			targetChannelID, ok := e.channelIDByOld[row.ChannelID]
			if !ok {
				e.unresolved("stats_channel_key", "", "channel_id", "source channel id %d was not mapped", row.ChannelID)
				return false
			}
			targetKeyID, ok := e.channelKeyIDByOld[row.ChannelKeyID]
			if !ok {
				e.unresolved("stats_channel_key", "", "channel_key_id", "source channel key id %d was not mapped", row.ChannelKeyID)
				return false
			}
			row.ChannelID = targetChannelID
			row.ChannelKeyID = targetKeyID
			return true
		},
		func(row model.StatsChannelKey) string { return fmt.Sprint(row.ChannelKeyID) }, false, true); err != nil {
		return err
	}
	if err := importStableRows(e, "stats_api_key", dump.StatsAPIKey,
		func(row *model.StatsAPIKey) bool {
			targetID, ok := e.apiKeyIDByOld[row.APIKeyID]
			if !ok {
				e.unresolved("stats_api_key", "", "api_key_id", "source API key id %d was not mapped", row.APIKeyID)
				return false
			}
			row.APIKeyID = targetID
			return true
		},
		func(row model.StatsAPIKey) string { return fmt.Sprint(row.APIKeyID) }, false, true); err != nil {
		return err
	}
	return e.importRelayLogs(dump)
}

func (e *dbImportV2Executor) importRelayLogs(dump *model.DBDump) error {
	if !dump.IncludeLogs {
		return nil
	}
	return importStableRows(e, "relay_logs", dump.RelayLogs,
		func(row *model.RelayLog) bool {
			row.Attempts = append([]model.ChannelAttempt(nil), row.Attempts...)
			if row.ChannelId != 0 {
				targetID, ok := e.channelIDByOld[row.ChannelId]
				if !ok {
					e.unresolved("relay_logs", "", "channel", "source channel id %d was not mapped", row.ChannelId)
					return false
				}
				row.ChannelId = targetID
			}
			for i := range row.Attempts {
				attempt := &row.Attempts[i]
				if attempt.ChannelID != 0 {
					targetID, ok := e.channelIDByOld[attempt.ChannelID]
					if !ok {
						e.unresolved("relay_logs", "", fmt.Sprintf("attempts[%d].channel_id", i), "source channel id %d was not mapped", attempt.ChannelID)
						return false
					}
					attempt.ChannelID = targetID
				}
				if attempt.ChannelKeyID != 0 {
					targetID, ok := e.channelKeyIDByOld[attempt.ChannelKeyID]
					if !ok {
						e.unresolved("relay_logs", "", fmt.Sprintf("attempts[%d].channel_key_id", i), "source channel key id %d was not mapped", attempt.ChannelKeyID)
						return false
					}
					attempt.ChannelKeyID = targetID
				}
			}
			return true
		},
		func(row model.RelayLog) string { return fmt.Sprint(row.ID) }, false, false)
}

func importStableRows[T any](
	e *dbImportV2Executor,
	table string,
	rows []T,
	prepare func(*T) bool,
	key func(T) string,
	alwaysUpdate bool,
	mergeUpdates bool,
) error {
	var existing []T
	if err := e.tx.Find(&existing).Error; err != nil {
		return fmt.Errorf("read target %s: %w", table, err)
	}
	existingByKey := make(map[string]T, len(existing))
	for _, row := range existing {
		existingByKey[key(row)] = row
	}
	for _, source := range rows {
		if !prepare(&source) {
			continue
		}
		rowKey := key(source)
		_, matched := existingByKey[rowKey]
		if matched {
			if !alwaysUpdate {
				e.conflict(table, "", "primary_key", "target row %s already exists", rowKey)
			}
			if !alwaysUpdate && (e.policy == model.DBImportConflictReject || e.policy == model.DBImportConflictSkip ||
				(e.policy == model.DBImportConflictMerge && !mergeUpdates)) {
				e.skip(table)
				continue
			}
			if !e.dryRun {
				if err := e.tx.Save(&source).Error; err != nil {
					return fmt.Errorf("update imported %s row %s: %w", table, rowKey, err)
				}
			}
			e.updated(table)
			continue
		}
		if !e.dryRun {
			if err := e.tx.Create(&source).Error; err != nil {
				return fmt.Errorf("create imported %s row %s: %w", table, rowKey, err)
			}
		}
		e.created(table)
	}
	return nil
}

func (e *dbImportV2Executor) rewriteSupportedModels(value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	parts := strings.Split(value, ",")
	for i, part := range parts {
		name := strings.TrimSpace(part)
		if mapped, ok := e.groupNameByOldName[name]; ok {
			parts[i] = mapped
		} else {
			parts[i] = name
		}
	}
	return strings.Join(parts, ",")
}

func (e *dbImportV2Executor) virtualID() int {
	id := e.nextVirtualID
	e.nextVirtualID--
	return id
}

func (e *dbImportV2Executor) created(table string) {
	summary := e.result.Tables[table]
	summary.Create++
	e.result.Tables[table] = summary
	e.result.RowsAffected[table]++
}

func (e *dbImportV2Executor) updated(table string) {
	summary := e.result.Tables[table]
	summary.Update++
	e.result.Tables[table] = summary
	e.result.RowsAffected[table]++
}

func (e *dbImportV2Executor) skip(table string) {
	summary := e.result.Tables[table]
	summary.Skip++
	e.result.Tables[table] = summary
}

func (e *dbImportV2Executor) deleted(table string) {
	summary := e.result.Tables[table]
	summary.Delete++
	e.result.Tables[table] = summary
	e.result.RowsAffected[table]++
}

func (e *dbImportV2Executor) conflict(table, entityUUID, field, format string, args ...any) {
	summary := e.result.Tables[table]
	summary.Conflict++
	e.result.Tables[table] = summary
	e.issue(model.DBImportIssue{Table: table, UUID: entityUUID, Field: field, Problem: fmt.Sprintf(format, args...)})
}

func (e *dbImportV2Executor) unresolved(table, entityUUID, field, format string, args ...any) {
	summary := e.result.Tables[table]
	summary.Unresolved++
	e.result.Tables[table] = summary
	e.issue(model.DBImportIssue{Table: table, UUID: entityUUID, Field: field, Problem: fmt.Sprintf(format, args...)})
}

func (e *dbImportV2Executor) issue(issue model.DBImportIssue) {
	if len(e.result.Issues) >= maxDBImportIssues {
		e.result.IssuesTruncated = true
		return
	}
	e.result.Issues = append(e.result.Issues, issue)
}

func (e *dbImportV2Executor) hasBlockingIssues() bool {
	if importResultCount(e.result, func(s model.DBImportTableSummary) int64 { return s.Unresolved }) > 0 {
		return true
	}
	return e.policy == model.DBImportConflictReject &&
		importResultCount(e.result, func(s model.DBImportTableSummary) int64 { return s.Conflict }) > 0
}

func importResultCount(result *model.DBImportResult, value func(model.DBImportTableSummary) int64) int64 {
	if result == nil {
		return 0
	}
	var total int64
	for _, summary := range result.Tables {
		total += value(summary)
	}
	return total
}

func canonicalUUID(value string) string {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return strings.TrimSpace(value)
	}
	return parsed.String()
}

func channelKeyNaturalKey(channelID int, value string) string {
	return fmt.Sprintf("%d\x00%s", channelID, value)
}

func groupItemNaturalKey(row model.GroupItem) string {
	return fmt.Sprintf("%d\x00%s\x00%d\x00%d\x00%s", row.GroupID, row.Type, row.ChannelID, row.TargetGroupID, row.ModelName)
}

// sortDBImportIssues makes reports deterministic across database dialects.
func sortDBImportIssues(result *model.DBImportResult) {
	if result == nil {
		return
	}
	sort.SliceStable(result.Issues, func(i, j int) bool {
		left, right := result.Issues[i], result.Issues[j]
		if left.Table != right.Table {
			return left.Table < right.Table
		}
		if left.UUID != right.UUID {
			return left.UUID < right.UUID
		}
		return left.Field < right.Field
	})
}
