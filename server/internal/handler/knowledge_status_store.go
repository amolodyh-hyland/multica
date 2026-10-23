package handler

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/redis/go-redis/v9"
)

const knowledgeStatusRetention = 35 * time.Second

type knowledgeStatusRecord struct {
	Request knowledge.StatusRequest  `json:"request"`
	State   string                   `json:"state"`
	Results []knowledge.SourceStatus `json:"results,omitempty"`
}

type KnowledgeStatusStore struct {
	redis   redis.UniversalClient
	mu      sync.Mutex
	records map[string][]byte
}

func NewKnowledgeStatusStore(client redis.UniversalClient) *KnowledgeStatusStore {
	return &KnowledgeStatusStore{redis: client, records: map[string][]byte{}}
}
func knowledgeStatusKey(runtimeID string) string { return "mul:knowledge_status:" + runtimeID }

func (s *KnowledgeStatusStore) create(ctx context.Context, runtimeID string, request knowledge.StatusRequest) (bool, error) {
	raw, err := json.Marshal(knowledgeStatusRecord{Request: request, State: "pending"})
	if err != nil {
		return false, err
	}
	if s.redis != nil {
		return s.redis.SetNX(ctx, knowledgeStatusKey(runtimeID), raw, knowledgeStatusRetention).Result()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.records {
		var saved knowledgeStatusRecord
		if json.Unmarshal(entry, &saved) != nil || !time.Now().Before(saved.Request.Deadline) {
			delete(s.records, key)
		}
	}
	if len(s.records) >= 128 || s.records[runtimeID] != nil {
		return false, nil
	}
	s.records[runtimeID] = raw
	return true, nil
}
func (s *KnowledgeStatusStore) read(ctx context.Context, runtimeID string) (*knowledgeStatusRecord, []byte, error) {
	var raw []byte
	var err error
	if s.redis != nil {
		raw, err = s.redis.Get(ctx, knowledgeStatusKey(runtimeID)).Bytes()
		if err == redis.Nil {
			return nil, nil, nil
		}
	} else {
		s.mu.Lock()
		raw = append([]byte(nil), s.records[runtimeID]...)
		s.mu.Unlock()
	}
	if err != nil {
		return nil, nil, err
	}
	if len(raw) == 0 {
		return nil, nil, nil
	}
	var record knowledgeStatusRecord
	if err = json.Unmarshal(raw, &record); err != nil {
		return nil, nil, err
	}
	if !time.Now().Before(record.Request.Deadline) {
		return nil, nil, nil
	}
	return &record, raw, nil
}

var knowledgeStatusCAS = redis.NewScript(`
if redis.call('GET',KEYS[1]) ~= ARGV[1] then return 0 end
if ARGV[2] == '' then redis.call('DEL',KEYS[1]) else redis.call('SET',KEYS[1],ARGV[2],'PX',35000) end
return 1
`)

func (s *KnowledgeStatusStore) swap(ctx context.Context, runtimeID string, old, new []byte) (bool, error) {
	if s.redis != nil {
		n, err := knowledgeStatusCAS.Run(ctx, s.redis, []string{knowledgeStatusKey(runtimeID)}, string(old), string(new)).Int()
		return n == 1, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if string(s.records[runtimeID]) != string(old) {
		return false, nil
	}
	if len(new) == 0 {
		delete(s.records, runtimeID)
	} else {
		s.records[runtimeID] = new
	}
	return true, nil
}
func (s *KnowledgeStatusStore) take(ctx context.Context, runtimeID string) (*knowledgeStatusRecord, error) {
	record, raw, err := s.read(ctx, runtimeID)
	if err != nil || record == nil || record.State != "pending" {
		return nil, err
	}
	record.State = "running"
	next, _ := json.Marshal(record)
	ok, err := s.swap(ctx, runtimeID, raw, next)
	if !ok {
		return nil, err
	}
	return record, err
}
func (s *KnowledgeStatusStore) finish(ctx context.Context, runtimeID, requestID string, results []knowledge.SourceStatus) (bool, error) {
	record, raw, err := s.read(ctx, runtimeID)
	if err != nil || record == nil || record.Request.ID != requestID || record.State != "running" {
		return false, err
	}
	record.State = "completed"
	record.Results = results
	next, _ := json.Marshal(record)
	return s.swap(ctx, runtimeID, raw, next)
}
func (s *KnowledgeStatusStore) remove(ctx context.Context, runtimeID, requestID string) {
	record, raw, err := s.read(ctx, runtimeID)
	if err == nil && record != nil && record.Request.ID == requestID {
		_, _ = s.swap(ctx, runtimeID, raw, nil)
	}
}
