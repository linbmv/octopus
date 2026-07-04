package health

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/utils/log"
)

// HealthSnapshot 健康状态快照（用于持久化）
type HealthSnapshot struct {
	Version   string                    `json:"version"`
	Timestamp time.Time                 `json:"timestamp"`
	States    map[string]HealthStateSnapshot `json:"states"`
}

// HealthStateSnapshot 单个健康状态快照
type HealthStateSnapshot struct {
	Key              HealthKey         `json:"key"`
	Stats            HealthStats       `json:"stats"`
	Score            float64           `json:"score"`
	EstimatorSnapshot EstimatorSnapshot `json:"estimator_snapshot"`
}

// PersistenceConfig 持久化配置
type PersistenceConfig struct {
	Enabled       bool          // 是否启用持久化
	DataDir       string        // 数据目录
	Interval      time.Duration // 持久化间隔
	MaxSnapshots  int           // 最大快照数量
}

// DefaultPersistenceConfig 默认持久化配置
func DefaultPersistenceConfig() PersistenceConfig {
	return PersistenceConfig{
		Enabled:      true,
		DataDir:      "./data/health",
		Interval:     5 * time.Minute,
		MaxSnapshots: 10,
	}
}

// HealthPersistence 健康状态持久化
type HealthPersistence struct {
	config  PersistenceConfig
	manager *HealthManager
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewHealthPersistence 创建持久化管理器
func NewHealthPersistence(config PersistenceConfig, manager *HealthManager) (*HealthPersistence, error) {
	if !config.Enabled {
		return nil, nil
	}

	// 确保数据目录存在
	if err := os.MkdirAll(config.DataDir, 0755); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &HealthPersistence{
		config:  config,
		manager: manager,
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

// Start 启动持久化
func (p *HealthPersistence) Start() {
	if p == nil {
		return
	}

	p.wg.Add(1)
	go p.persistLoop()

	log.Infof("Health persistence started: interval=%v, dir=%s", p.config.Interval, p.config.DataDir)
}

// Stop 停止持久化
func (p *HealthPersistence) Stop() {
	if p == nil {
		return
	}

	p.cancel()
	p.wg.Wait()

	// 最后一次保存
	if err := p.Save(); err != nil {
		log.Errorf("Failed to save health states on shutdown: %v", err)
	}

	log.Infof("Health persistence stopped")
}

// persistLoop 持久化循环
func (p *HealthPersistence) persistLoop() {
	defer p.wg.Done()

	ticker := time.NewTicker(p.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			if err := p.Save(); err != nil {
				log.Errorf("Failed to persist health states: %v", err)
			}
		}
	}
}

// Save 保存健康状态
func (p *HealthPersistence) Save() error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// 创建快照
	snapshot := p.createSnapshot()

	// 生成文件名
	filename := filepath.Join(p.config.DataDir, time.Now().Format("health_20060102_150405.json"))

	// 写入文件
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return err
	}

	// 清理旧快照
	p.cleanupOldSnapshots()

	log.Debugf("Health states persisted: %s", filename)
	return nil
}

// createSnapshot 创建快照
func (p *HealthPersistence) createSnapshot() HealthSnapshot {
	allStates := p.manager.GetAllStates()

	states := make(map[string]HealthStateSnapshot, len(allStates))
	for key, stats := range allStates {
		// 生成唯一键
		keyStr := makeKeyString(key)

		// 获取 estimator 快照
		health, ok := p.manager.Get(key)
		var estimatorSnapshot EstimatorSnapshot
		if ok {
			estimatorSnapshot = health.Stats.Estimator.Snapshot()
		}

		states[keyStr] = HealthStateSnapshot{
			Key:              key,
			Stats:            stats,
			Score:            health.GetScore(),
			EstimatorSnapshot: estimatorSnapshot,
		}
	}

	return HealthSnapshot{
		Version:   "v1",
		Timestamp: time.Now(),
		States:    states,
	}
}

// Load 加载健康状态
func (p *HealthPersistence) Load() error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// 查找最新快照
	filename, err := p.findLatestSnapshot()
	if err != nil {
		return err
	}

	if filename == "" {
		log.Infof("No health snapshot found, starting fresh")
		return nil
	}

	// 读取文件
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}

	// 解析快照
	var snapshot HealthSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}

	// 恢复状态
	count := 0
	for _, stateSnapshot := range snapshot.States {
		health := p.manager.GetOrCreate(stateSnapshot.Key)

		// 恢复 estimator
		if len(stateSnapshot.EstimatorSnapshot.Data) > 0 {
			if err := health.Stats.Estimator.Restore(stateSnapshot.EstimatorSnapshot); err != nil {
				log.Warnf("Failed to restore estimator for %+v: %v", stateSnapshot.Key, err)
				continue
			}
		}


		health.RestoreStats(stateSnapshot.Stats, stateSnapshot.Score)

		count++
	}

	log.Infof("Health states restored: %d states from %s (age: %v)",
		count, filepath.Base(filename), time.Since(snapshot.Timestamp))

	return nil
}

// findLatestSnapshot 查找最新快照
func (p *HealthPersistence) findLatestSnapshot() (string, error) {
	entries, err := os.ReadDir(p.config.DataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	var latest string
	var latestTime time.Time

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latest = filepath.Join(p.config.DataDir, name)
		}
	}

	return latest, nil
}

// cleanupOldSnapshots 清理旧快照
func (p *HealthPersistence) cleanupOldSnapshots() {
	entries, err := os.ReadDir(p.config.DataDir)
	if err != nil {
		return
	}

	// 收集所有快照文件
	type snapshotFile struct {
		name    string
		modTime time.Time
	}

	var snapshots []snapshotFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		snapshots = append(snapshots, snapshotFile{
			name:    filepath.Join(p.config.DataDir, name),
			modTime: info.ModTime(),
		})
	}

	// 如果快照数量没有超过限制，直接返回
	if len(snapshots) <= p.config.MaxSnapshots {
		return
	}

	// 删除最旧的快照，直到数量达到限制
	for len(snapshots) > p.config.MaxSnapshots {
		// 找到最旧的快照
		oldest := 0
		for i := 1; i < len(snapshots); i++ {
			if snapshots[i].modTime.Before(snapshots[oldest].modTime) {
				oldest = i
			}
		}

		// 删除文件
		if err := os.Remove(snapshots[oldest].name); err != nil {
			log.Warnf("Failed to remove old snapshot %s: %v", snapshots[oldest].name, err)
		}

		// 从列表移除
		snapshots = append(snapshots[:oldest], snapshots[oldest+1:]...)
	}
}

// makeKeyString 生成唯一键字符串
func makeKeyString(key HealthKey) string {
	return fmt.Sprintf("%d_%d_%s", key.ChannelID, key.KeyID, key.Model)
}
