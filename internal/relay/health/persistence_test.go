package health

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHealthPersistence_SaveAndLoad 测试保存和加载
func TestHealthPersistence_SaveAndLoad(t *testing.T) {
	// 创建临时目录
	tmpDir := t.TempDir()

	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	// 添加一些状态
	manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)
	manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)
	manager.RecordSuccess(2, 200, "gpt-3.5", 2*time.Second)

	// 创建持久化管理器
	persistConfig := PersistenceConfig{
		Enabled:      true,
		DataDir:      tmpDir,
		Interval:     1 * time.Minute,
		MaxSnapshots: 3,
	}

	persistence, err := NewHealthPersistence(persistConfig, manager)
	if err != nil {
		t.Fatalf("Failed to create persistence: %v", err)
	}

	// 保存
	if err := persistence.Save(); err != nil {
		t.Fatalf("Failed to save: %v", err)
	}

	// 验证文件存在
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read dir: %v", err)
	}

	if len(entries) != 1 {
		t.Errorf("Expected 1 snapshot file, got %d", len(entries))
	}

	// 创建新的管理器并加载
	manager2 := NewHealthManager(config)
	persistence2, err := NewHealthPersistence(persistConfig, manager2)
	if err != nil {
		t.Fatalf("Failed to create persistence2: %v", err)
	}

	if err := persistence2.Load(); err != nil {
		t.Fatalf("Failed to load: %v", err)
	}

	// 验证状态已恢复
	allStates := manager2.GetAllStates()
	if len(allStates) != 2 {
		t.Errorf("Expected 2 states, got %d", len(allStates))
	}

	// 验证具体状态
	key1 := HealthKey{ChannelID: 1, KeyID: 100, Model: "gpt-4"}
	health1, ok := manager2.Get(key1)
	if !ok {
		t.Error("Expected state for channel 1 to exist")
	} else {
		stats := health1.GetStats()
		if stats.SuccessCount != 2 {
			t.Errorf("Expected 2 successes for channel 1, got %d", stats.SuccessCount)
		}
	}
}

// TestHealthPersistence_CleanupOldSnapshots 测试清理旧快照
func TestHealthPersistence_CleanupOldSnapshots(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	persistConfig := PersistenceConfig{
		Enabled:      true,
		DataDir:      tmpDir,
		Interval:     1 * time.Minute,
		MaxSnapshots: 3,
	}

	persistence, err := NewHealthPersistence(persistConfig, manager)
	if err != nil {
		t.Fatalf("Failed to create persistence: %v", err)
	}

	// 添加一些初始数据
	manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)
	manager.RecordSuccess(2, 200, "gpt-3.5", 2*time.Second)

	// 创建 5 个快照
	for i := 0; i < 5; i++ {
		if err := persistence.Save(); err != nil {
			t.Fatalf("Failed to save snapshot %d: %v", i, err)
		}
		time.Sleep(50 * time.Millisecond) // 确保文件时间不同
	}

	// 验证只保留 3 个快照
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read dir: %v", err)
	}

	jsonCount := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			jsonCount++
		}
	}

	// 因为每次 Save 都会清理，所以最终应该只有最后 3 个
	if jsonCount > persistConfig.MaxSnapshots {
		t.Errorf("Expected at most %d snapshot files, got %d", persistConfig.MaxSnapshots, jsonCount)
	}

	t.Logf("Snapshot count after cleanup: %d (max: %d)", jsonCount, persistConfig.MaxSnapshots)
}

// TestHealthPersistence_Disabled 测试禁用持久化
func TestHealthPersistence_Disabled(t *testing.T) {
	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	persistConfig := PersistenceConfig{
		Enabled: false,
	}

	persistence, err := NewHealthPersistence(persistConfig, manager)
	if err != nil {
		t.Fatalf("Failed to create persistence: %v", err)
	}

	if persistence != nil {
		t.Error("Expected nil persistence when disabled")
	}
}

// TestHealthPersistence_StartStop 测试启动和停止
func TestHealthPersistence_StartStop(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	persistConfig := PersistenceConfig{
		Enabled:      true,
		DataDir:      tmpDir,
		Interval:     100 * time.Millisecond,
		MaxSnapshots: 3,
	}

	persistence, err := NewHealthPersistence(persistConfig, manager)
	if err != nil {
		t.Fatalf("Failed to create persistence: %v", err)
	}

	// 启动
	persistence.Start()

	// 添加状态
	manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)

	// 等待至少一次持久化
	time.Sleep(200 * time.Millisecond)

	// 停止
	persistence.Stop()

	// 验证文件存在
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read dir: %v", err)
	}

	if len(entries) == 0 {
		t.Error("Expected at least one snapshot file")
	}
}

// TestHealthPersistence_EstimatorRestore 测试 estimator 恢复
func TestHealthPersistence_EstimatorRestore(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultHealthConfig()
	config.MinSamplesForAdaptiveTimeout = 5
	manager := NewHealthManager(config)

	// 添加足够样本以触发自适应超时
	for i := 0; i < 20; i++ {
		manager.RecordSuccess(1, 100, "gpt-4", time.Duration(3000+i*10)*time.Millisecond)
	}

	// 获取原始超时
	originalTimeout := manager.GetTimeout(1, 100, "gpt-4")
	t.Logf("Original timeout: %v", originalTimeout)

	// 保存
	persistConfig := PersistenceConfig{
		Enabled:      true,
		DataDir:      tmpDir,
		Interval:     1 * time.Minute,
		MaxSnapshots: 3,
	}

	persistence, err := NewHealthPersistence(persistConfig, manager)
	if err != nil {
		t.Fatalf("Failed to create persistence: %v", err)
	}

	if err := persistence.Save(); err != nil {
		t.Fatalf("Failed to save: %v", err)
	}

	// 加载到新管理器
	manager2 := NewHealthManager(config)
	persistence2, err := NewHealthPersistence(persistConfig, manager2)
	if err != nil {
		t.Fatalf("Failed to create persistence2: %v", err)
	}

	if err := persistence2.Load(); err != nil {
		t.Fatalf("Failed to load: %v", err)
	}

	// 验证 estimator 已恢复
	restoredTimeout := manager2.GetTimeout(1, 100, "gpt-4")
	t.Logf("Restored timeout: %v", restoredTimeout)

	// 超时应该相近（允许 10% 误差）
	diff := float64(originalTimeout - restoredTimeout)
	if diff < 0 {
		diff = -diff
	}
	tolerance := float64(originalTimeout) * 0.1

	if diff > tolerance {
		t.Errorf("Timeout mismatch: original=%v, restored=%v", originalTimeout, restoredTimeout)
	}
}

// TestHealthPersistence_NoSnapshotFound 测试没有快照时的行为
func TestHealthPersistence_NoSnapshotFound(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	persistConfig := PersistenceConfig{
		Enabled:      true,
		DataDir:      tmpDir,
		Interval:     1 * time.Minute,
		MaxSnapshots: 3,
	}

	persistence, err := NewHealthPersistence(persistConfig, manager)
	if err != nil {
		t.Fatalf("Failed to create persistence: %v", err)
	}

	// 加载（没有快照）
	if err := persistence.Load(); err != nil {
		t.Errorf("Load should not fail when no snapshot found: %v", err)
	}

	// 验证状态为空
	allStates := manager.GetAllStates()
	if len(allStates) != 0 {
		t.Errorf("Expected 0 states, got %d", len(allStates))
	}
}

// TestHealthPersistence_CorruptedSnapshot 测试损坏快照的处理
func TestHealthPersistence_CorruptedSnapshot(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	persistConfig := PersistenceConfig{
		Enabled:      true,
		DataDir:      tmpDir,
		Interval:     1 * time.Minute,
		MaxSnapshots: 3,
	}

	persistence, err := NewHealthPersistence(persistConfig, manager)
	if err != nil {
		t.Fatalf("Failed to create persistence: %v", err)
	}

	// 创建损坏的快照文件
	corruptedFile := filepath.Join(tmpDir, "health_corrupted.json")
	if err := os.WriteFile(corruptedFile, []byte("invalid json"), 0644); err != nil {
		t.Fatalf("Failed to write corrupted file: %v", err)
	}

	// 加载应该失败
	if err := persistence.Load(); err == nil {
		t.Error("Expected error when loading corrupted snapshot")
	}
}
