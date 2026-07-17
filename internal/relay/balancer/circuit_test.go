package balancer

import (
	"sync"
	"testing"
	"time"
)

// circuitTestClock 提供可控时钟并在测试结束后恢复真实时钟与全局状态。
func circuitTestClock(t *testing.T, start time.Time) func(time.Duration) {
	t.Helper()
	current := start
	var mu sync.Mutex
	circuitNow = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	globalBreaker.clear()
	t.Cleanup(func() {
		circuitNow = time.Now
		globalBreaker.clear()
	})
	return func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		current = current.Add(d)
	}
}

// tripBreaker 用默认阈值（2 次连续失败）把 (1,1,model) 打入 Open。
func tripBreaker(t *testing.T, modelName string) {
	t.Helper()
	RecordFailure(1, 1, modelName)
	RecordFailure(1, 1, modelName)
	if tripped, _ := IsTripped(1, 1, modelName); !tripped {
		t.Fatal("熔断器应在两次连续失败后进入 Open")
	}
}

func TestCircuitOpensAfterThresholdAndCooldownGrantsProbe(t *testing.T) {
	advance := circuitTestClock(t, time.Unix(1000000, 0))
	tripBreaker(t, "m")

	// 冷却期内持续拒绝并报告剩余时间。
	if tripped, remaining := IsTripped(1, 1, "m"); !tripped || remaining <= 0 {
		t.Fatalf("冷却期内应 tripped 且 remaining>0，got tripped=%v remaining=%v", tripped, remaining)
	}

	// 默认基础冷却 60s（无 DB 设置时的回退值）。
	advance(61 * time.Second)
	if tripped, _ := IsTripped(1, 1, "m"); tripped {
		t.Fatal("冷却期结束后第一个请求应被授予试探资格")
	}
}

func TestHalfOpenAllowsBoundedConcurrentProbes(t *testing.T) {
	advance := circuitTestClock(t, time.Unix(1000000, 0))
	tripBreaker(t, "m")
	advance(61 * time.Second)

	// 默认 halfOpenMaxProbes=2：Open->HalfOpen 授予第 1 个，HalfOpen 内授予第 2 个。
	if tripped, _ := IsTripped(1, 1, "m"); tripped {
		t.Fatal("第 1 个试探应放行")
	}
	if tripped, _ := IsTripped(1, 1, "m"); tripped {
		t.Fatal("第 2 个试探应放行（并发试探上限默认 2）")
	}
	if tripped, _ := IsTripped(1, 1, "m"); !tripped {
		t.Fatal("超出并发试探上限后应拒绝")
	}
}

func TestRecordProbeAbortReleasesSlot(t *testing.T) {
	advance := circuitTestClock(t, time.Unix(1000000, 0))
	tripBreaker(t, "m")
	advance(61 * time.Second)

	IsTripped(1, 1, "m") // probe 1
	IsTripped(1, 1, "m") // probe 2（占满）
	if tripped, _ := IsTripped(1, 1, "m"); !tripped {
		t.Fatal("名额占满时应拒绝")
	}

	// 模拟试探请求在到达上游前被跳过/取消：归还名额后应立即允许新试探。
	RecordProbeAbort(1, 1, "m")
	if tripped, _ := IsTripped(1, 1, "m"); tripped {
		t.Fatal("归还试探名额后应立即放行新试探")
	}
}

func TestHalfOpenProbeLeaseExpiryGrantsReplacement(t *testing.T) {
	advance := circuitTestClock(t, time.Unix(1000000, 0))
	tripBreaker(t, "m")
	advance(61 * time.Second)

	IsTripped(1, 1, "m") // probe 1
	IsTripped(1, 1, "m") // probe 2（占满）

	// 场景 F1 回归：两个试探都"消失"（客户端取消且历史代码未结算）。
	// 租约（默认 60s）到期后必须放行替补试探，而不是永久冻结。
	advance(61 * time.Second)
	if tripped, _ := IsTripped(1, 1, "m"); tripped {
		t.Fatal("试探租约到期后应放行替补试探（修复前会永久卡在 HalfOpen）")
	}

	// 替补成功后熔断关闭，恢复正常放行。
	RecordSuccess(1, 1, "m")
	if tripped, _ := IsTripped(1, 1, "m"); tripped {
		t.Fatal("试探成功后应回到 Closed")
	}
}

func TestHalfOpenProbeFailureReopensWithBackoff(t *testing.T) {
	advance := circuitTestClock(t, time.Unix(1000000, 0))
	tripBreaker(t, "m")
	advance(61 * time.Second)

	IsTripped(1, 1, "m") // 授予试探
	RecordFailure(1, 1, "m")

	// 试探失败 → Open，且 TripCount=2 → 冷却翻倍为 120s。
	if tripped, remaining := IsTripped(1, 1, "m"); !tripped || remaining <= 60*time.Second {
		t.Fatalf("试探失败后应重新熔断且冷却指数退避，got tripped=%v remaining=%v", tripped, remaining)
	}
	advance(121 * time.Second)
	if tripped, _ := IsTripped(1, 1, "m"); tripped {
		t.Fatal("退避冷却结束后应再次授予试探")
	}
}

func TestRecordProbeAbortOnClosedEntryIsNoop(t *testing.T) {
	circuitTestClock(t, time.Unix(1000000, 0))
	RecordFailure(1, 1, "m") // 1 次失败，仍 Closed

	RecordProbeAbort(1, 1, "m")
	RecordProbeAbort(2, 2, "absent") // 不存在的条目

	if tripped, _ := IsTripped(1, 1, "m"); tripped {
		t.Fatal("Closed 条目上的 abort 必须是空操作")
	}
}

func TestResetCircuitClearsChannelModel(t *testing.T) {
	circuitTestClock(t, time.Unix(1000000, 0))
	tripBreaker(t, "frozen-model")
	// 同渠道另一模型、另一渠道同名模型：不应被误清。
	RecordFailure(1, 2, "other-model")
	RecordFailure(1, 2, "other-model")
	RecordFailure(2, 1, "frozen-model")
	RecordFailure(2, 1, "frozen-model")

	ResetCircuit(1, "frozen-model")

	if tripped, _ := IsTripped(1, 1, "frozen-model"); tripped {
		t.Fatal("ResetCircuit 后目标 (channel=1, model) 应立即可用")
	}
	if tripped, _ := IsTripped(1, 2, "other-model"); !tripped {
		t.Fatal("同渠道其他模型的熔断不应被清除")
	}
	if tripped, _ := IsTripped(2, 1, "frozen-model"); !tripped {
		t.Fatal("其他渠道同名模型的熔断不应被清除")
	}
}

func TestResetCircuitEmptyModelClearsWholeChannel(t *testing.T) {
	circuitTestClock(t, time.Unix(1000000, 0))
	tripBreaker(t, "m1")
	RecordFailure(1, 2, "m2")
	RecordFailure(1, 2, "m2")

	ResetCircuit(1, "")

	if tripped, _ := IsTripped(1, 1, "m1"); tripped {
		t.Fatal("空模型名应清除渠道全部熔断条目 (m1)")
	}
	if tripped, _ := IsTripped(1, 2, "m2"); tripped {
		t.Fatal("空模型名应清除渠道全部熔断条目 (m2)")
	}
}

func TestHalfOpenConcurrentIsTrippedGrantsExactlyMaxProbes(t *testing.T) {
	advance := circuitTestClock(t, time.Unix(1000000, 0))
	tripBreaker(t, "m")
	advance(61 * time.Second)

	const goroutines = 32
	granted := make(chan struct{}, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tripped, _ := IsTripped(1, 1, "m"); !tripped {
				granted <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(granted)
	count := 0
	for range granted {
		count++
	}
	if count != 2 {
		t.Fatalf("并发下应恰好授予 2 个试探（默认上限），got %d", count)
	}
}
