// Package tool — Web 搜索工具：通过搜索引擎 API 搜索实时信息。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WebSearchConfig 配置 web 搜索工具。
type WebSearchConfig struct {
	// Engine 搜索引擎类型：brave / google / searxng
	Engine string
	// APIKey 搜索 API 的密钥
	APIKey string
	// BaseURL 自定义搜索 API 地址（SearXNG 自部署时必填）
	BaseURL string
}

// WebSearch 返回 web 搜索工具。
// 支持 Brave Search API、Google Custom Search API、SearXNG。
func WebSearch(cfg WebSearchConfig) Tool {
	client := &http.Client{Timeout: 15 * time.Second}

	return Tool{
		Name:        "web_search",
		Description: "Search the web for real-time information. Use this when you need current data, documentation, or facts not in your training data.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query",
				},
				"max_results": map[string]any{
					"type":        "number",
					"description": "Maximum number of results (default 5, max 10)",
				},
			},
			"required": []string{"query"},
		},
		Execute: func(ctx context.Context, args Args) Result {
			query, err := MustGetString(args, "query")
			if err != nil {
				return Errf("%v", err)
			}
			maxResults := 5
			if v, ok := args["max_results"]; ok {
				if f, ok := v.(float64); ok && f >= 1 && f <= 10 {
					maxResults = int(f)
				}
			}

			engine := strings.ToLower(cfg.Engine)
			switch engine {
			case "brave":
				return searchBrave(ctx, client, cfg.APIKey, query, maxResults)
			case "searxng":
				return searchSearXNG(ctx, client, cfg.BaseURL, query, maxResults)
			default:
				return searchBrave(ctx, client, cfg.APIKey, query, maxResults)
			}
		},
	}
}

// --- Brave Search API ---

func searchBrave(ctx context.Context, client *http.Client, apiKey, query string, maxResults int) Result {
	if apiKey == "" {
		return Errf("web_search: Brave API key not configured (set web_search.api_key)")
	}
	u := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d",
		url.QueryEscape(query), maxResults)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return Errf("web_search: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return Errf("web_search: request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return Errf("web_search: read response: %v", err)
	}
	if resp.StatusCode != 200 {
		return Errf("web_search: Brave API returned HTTP %d: %s", resp.StatusCode, truncateLine(string(body), 200))
	}

	var braveResp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &braveResp); err != nil {
		return Errf("web_search: parse response: %v", err)
	}

	items := make([]searchItem, 0, len(braveResp.Web.Results))
	for _, r := range braveResp.Web.Results {
		items = append(items, searchItem{Title: r.Title, URL: r.URL, Description: r.Description})
	}
	return formatSearchResults(items, maxResults)
}

// --- SearXNG (self-hosted) ---

func searchSearXNG(ctx context.Context, client *http.Client, baseURL, query string, maxResults int) Result {
	if baseURL == "" {
		return Errf("web_search: SearXNG base_url not configured")
	}
	u := fmt.Sprintf("%s/search?q=%s&format=json&pageno=1",
		strings.TrimRight(baseURL, "/"), url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return Errf("web_search: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return Errf("web_search: request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return Errf("web_search: read response: %v", err)
	}
	if resp.StatusCode != 200 {
		return Errf("web_search: SearXNG returned HTTP %d", resp.StatusCode)
	}

	var searxResp struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &searxResp); err != nil {
		return Errf("web_search: parse response: %v", err)
	}

	items := make([]searchItem, 0, len(searxResp.Results))
	for _, r := range searxResp.Results {
		items = append(items, searchItem{Title: r.Title, URL: r.URL, Description: r.Content})
	}
	return formatSearchResults(items, maxResults)
}

// searchItem 是搜索结果的统一结构。
type searchItem struct {
	Title       string
	URL         string
	Description string
}

// formatSearchResults 将搜索结果格式化为用户可读文本。
func formatSearchResults(results []searchItem, maxResults int) Result {
	if len(results) == 0 {
		return OK("No search results found.")
	}
	var sb strings.Builder
	for i, r := range results {
		if i >= maxResults {
			break
		}
		fmt.Fprintf(&sb, "%d. **%s**\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Description)
	}
	return OK(sb.String())
}
