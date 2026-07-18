package balancer

import (
	"container/list"
	"strings"
	"sync"
	"time"
)

const (
	// stickyStateLimit bounds request-model affinity state even when clients
	// continuously generate unique model names.
	stickyStateLimit  = 8192
	stickyStateMaxAge = 24 * time.Hour
)

var sessionNow = time.Now

// SessionEntry 会话保持条目
type SessionEntry struct {
	ChannelID    int
	ChannelKeyID int
	ModelName    string // 成功时使用的实际上游模型名，用于命中时校验，避免同渠道多模型导致 prompt cache miss
	Timestamp    time.Time
}

type sessionCacheKey struct {
	APIKeyID int
	Model    string
	Session  string
}

type sessionRecord struct {
	key   sessionCacheKey
	entry SessionEntry
}

type sessionState struct {
	mu         sync.Mutex
	entries    map[sessionCacheKey]*list.Element
	order      *list.List // oldest SetSticky call first
	operations uint64
}

func newSessionState() *sessionState {
	return &sessionState{
		entries: make(map[sessionCacheKey]*list.Element),
		order:   list.New(),
	}
}

// 全局会话存储。Map 与淘汰队列由同一把锁保护，容量检查和写入是原子的。
var globalSession = newSessionState()

// sessionKey 生成规范化会话键。模型名不区分首尾空白和大小写，避免同一
// 请求模型因为客户端格式差异不断创建新条目。
func sessionKey(apiKeyID int, requestModel string) sessionCacheKey {
	return sessionKeyForID(apiKeyID, requestModel, "")
}

func sessionKeyForID(apiKeyID int, requestModel, sessionID string) sessionCacheKey {
	return sessionCacheKey{
		APIKeyID: apiKeyID,
		Model:    strings.ToLower(strings.TrimSpace(requestModel)),
		Session:  strings.TrimSpace(sessionID),
	}
}

// GetSticky 获取粘性通道（ttl 内有效）
// ttl 由 Group.SessionKeepTime 决定，返回 nil 表示无有效会话。
func GetSticky(apiKeyID int, requestModel string, ttl time.Duration) *SessionEntry {
	return globalSession.get(sessionKey(apiKeyID, requestModel), ttl, sessionNow())
}

func GetStickyForSession(apiKeyID int, requestModel, sessionID string, ttl time.Duration) *SessionEntry {
	return globalSession.get(sessionKeyForID(apiKeyID, requestModel, sessionID), ttl, sessionNow())
}

func (s *sessionState) get(key sessionCacheKey, ttl time.Duration, now time.Time) *SessionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	element, ok := s.entries[key]
	if !ok {
		return nil
	}
	record := element.Value.(*sessionRecord)
	if ttl <= 0 || stateExpired(record.entry.Timestamp, now, ttl) || stateExpired(record.entry.Timestamp, now, stickyStateMaxAge) {
		s.removeElementLocked(element)
		return nil
	}

	// Do not expose the shared cache value to callers. SessionEntry currently
	// contains value fields only, so a shallow copy is sufficient.
	entry := record.entry
	return &entry
}

// SetSticky 写入/更新粘性记录
// actualModel 为本次成功 attempt 使用的实际上游模型名，命中复用时需与候选模型一致。
func SetSticky(apiKeyID int, requestModel string, channelID, keyID int, actualModel string) {
	SetStickyForSession(apiKeyID, requestModel, "", channelID, keyID, actualModel)
}

func SetStickyForSession(apiKeyID int, requestModel, sessionID string, channelID, keyID int, actualModel string) {
	globalSession.set(sessionKeyForID(apiKeyID, requestModel, sessionID), SessionEntry{
		ChannelID:    channelID,
		ChannelKeyID: keyID,
		ModelName:    actualModel,
		Timestamp:    sessionNow(),
	})
}

func (s *sessionState) set(key sessionCacheKey, entry SessionEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.operations++
	if s.operations%runtimeStateSweepEvery == 0 {
		s.sweepExpiredLocked(entry.Timestamp)
	}

	if element, ok := s.entries[key]; ok {
		element.Value.(*sessionRecord).entry = entry
		s.order.MoveToBack(element)
		return
	}

	if len(s.entries) >= stickyStateLimit {
		s.sweepExpiredLocked(entry.Timestamp)
	}
	if len(s.entries) >= stickyStateLimit {
		s.removeElementLocked(s.order.Front())
	}

	element := s.order.PushBack(&sessionRecord{key: key, entry: entry})
	s.entries[key] = element
}

func (s *sessionState) sweepExpiredLocked(now time.Time) {
	for element := s.order.Front(); element != nil; {
		next := element.Next()
		record := element.Value.(*sessionRecord)
		if stateExpired(record.entry.Timestamp, now, stickyStateMaxAge) {
			s.removeElementLocked(element)
		}
		element = next
	}
}

func (s *sessionState) invalidateAPIKey(apiKeyID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for element := s.order.Front(); element != nil; {
		next := element.Next()
		if element.Value.(*sessionRecord).key.APIKeyID == apiKeyID {
			s.removeElementLocked(element)
		}
		element = next
	}
}

func (s *sessionState) invalidateChannel(channelID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for element := s.order.Front(); element != nil; {
		next := element.Next()
		if element.Value.(*sessionRecord).entry.ChannelID == channelID {
			s.removeElementLocked(element)
		}
		element = next
	}
}

func (s *sessionState) removeElementLocked(element *list.Element) {
	if element == nil {
		return
	}
	record := element.Value.(*sessionRecord)
	delete(s.entries, record.key)
	s.order.Remove(element)
}

func (s *sessionState) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = make(map[sessionCacheKey]*list.Element)
	s.order.Init()
	s.operations = 0
}

func (s *sessionState) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
