// Package memory — 向量增强记忆：基于 embedding 余弦相似度的语义检索。
//
// 当配置了 Embedder 时，Add 会自动计算 embedding 向量并存储；
// SemanticSearch 使用余弦相似度进行语义检索。
// 未配置 Embedder 时退化为关键词匹配。
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Embedder 是向量编码接口。
type Embedder interface {
	// Embed 将文本编码为向量。
	Embed(ctx context.Context, text string) ([]float64, error)
}

// VectorEntry 带向量的记忆条目。
type VectorEntry struct {
	Entry
	Vector []float64 `json:"vector,omitempty"`
}

// VectorStore 是向量增强的记忆存储。
type VectorStore struct {
	mu       sync.RWMutex
	entries  []VectorEntry
	embedder Embedder
	dir      string
	nextID   int
}

// NewVectorStore 创建向量记忆存储。embedder 可为 nil（退化为关键词匹配）。
func NewVectorStore(dir string, embedder Embedder) *VectorStore {
	vs := &VectorStore{dir: dir, embedder: embedder}
	if dir != "" {
		vs.load()
	}
	return vs
}

// Add 添加记忆条目，自动计算 embedding（若 embedder 可用）。
func (vs *VectorStore) Add(ctx context.Context, content string, tags ...string) string {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	vs.nextID++
	ve := VectorEntry{
		Entry: Entry{
			ID:        fmt.Sprintf("mem_%d", vs.nextID),
			Content:   content,
			Tags:      tags,
			CreatedAt: time.Now(),
		},
	}

	// 计算 embedding
	if vs.embedder != nil {
		vec, err := vs.embedder.Embed(ctx, content)
		if err != nil {
			slog.Warn("embedding failed, storing without vector", "err", err)
		} else {
			ve.Vector = vec
		}
	}

	vs.entries = append(vs.entries, ve)
	vs.save()
	return ve.ID
}

// SemanticSearch 使用余弦相似度进行语义检索。
// 若 embedder 不可用或查询编码失败，退化为关键词匹配。
func (vs *VectorStore) SemanticSearch(ctx context.Context, query string, maxResults int) []Entry {
	if maxResults <= 0 {
		maxResults = 6
	}

	// 尝试向量搜索
	if vs.embedder != nil {
		qvec, err := vs.embedder.Embed(ctx, query)
		if err == nil {
			return vs.cosinSearch(qvec, maxResults)
		}
		slog.Debug("embedding query failed, falling back to keyword search", "err", err)
	}

	// 退化：复用关键词搜索
	return vs.keywordSearch(query, maxResults)
}

// cosinSearch 使用余弦相似度排序。
func (vs *VectorStore) cosinSearch(qvec []float64, maxResults int) []Entry {
	vs.mu.RLock()
	defer vs.mu.RUnlock()

	type scored struct {
		entry Entry
		sim   float64
	}
	var matches []scored

	for _, ve := range vs.entries {
		if len(ve.Vector) == 0 {
			continue
		}
		sim := cosineSimilarity(qvec, ve.Vector)
		if sim > 0.3 { // 相似度阈值
			matches = append(matches, scored{entry: ve.Entry, sim: sim})
		}
	}

	// 按相似度降序排序
	for i := 0; i < len(matches); i++ {
		for j := i + 1; j < len(matches); j++ {
			if matches[j].sim > matches[i].sim {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}

	result := make([]Entry, 0, maxResults)
	for i := 0; i < len(matches) && i < maxResults; i++ {
		result = append(result, matches[i].entry)
	}
	return result
}

// keywordSearch 关键词匹配搜索（退化方案）。
func (vs *VectorStore) keywordSearch(query string, maxResults int) []Entry {
	// 转为普通 Store 的搜索
	vs.mu.RLock()
	entries := make([]Entry, len(vs.entries))
	for i, ve := range vs.entries {
		entries[i] = ve.Entry
	}
	vs.mu.RUnlock()

	// 创建临时 Store 复用搜索逻辑
	tmpStore := &Store{entries: entries}
	return tmpStore.Search(query, maxResults)
}

func (vs *VectorStore) save() {
	if vs.dir == "" {
		return
	}
	if err := os.MkdirAll(vs.dir, 0o700); err != nil {
		return
	}
	data, err := json.MarshalIndent(vs.entries, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(vs.dir, "vectors.json"), data, 0o600)
}

func (vs *VectorStore) load() {
	data, err := os.ReadFile(filepath.Join(vs.dir, "vectors.json"))
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &vs.entries)
	for _, e := range vs.entries {
		var n int
		if _, err := fmt.Sscanf(e.ID, "mem_%d", &n); err == nil && n > vs.nextID {
			vs.nextID = n
		}
	}
}

// --- 余弦相似度 ---

func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// --- OpenAI Embedder 实现 ---

// OpenAIEmbedder 使用 OpenAI 兼容 API 获取文本向量。
type OpenAIEmbedder struct {
	BaseURL string
	APIKey  string
	Model   string
	client  *http.Client
}

// NewOpenAIEmbedder 创建 OpenAI embedding 客户端。
func NewOpenAIEmbedder(baseURL, apiKey, model string) *OpenAIEmbedder {
	if model == "" {
		model = "text-embedding-3-small"
	}
	return &OpenAIEmbedder{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Embed 调用 OpenAI embeddings API 将文本编码为向量。
func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	body, _ := json.Marshal(map[string]any{
		"model": e.Model,
		"input": text,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", e.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("embeddings API status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}
	return result.Data[0].Embedding, nil
}
