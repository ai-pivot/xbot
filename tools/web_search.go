package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"xbot/llm"
)

// WebSearchTool web search tool (Tavily API)
type WebSearchTool struct {
	apiKey     string
	httpClient *http.Client
}

// NewWebSearchTool creates a web search tool
func NewWebSearchTool(apiKey string) *WebSearchTool {
	return &WebSearchTool{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: httpDefaultTimeout,
		},
	}
}

// SetAPIKey updates the Tavily API key at runtime.
func (t *WebSearchTool) SetAPIKey(key string) {
	t.apiKey = key
}

func (t *WebSearchTool) Name() string {
	return "WebSearch"
}

func (t *WebSearchTool) Description() string {
	return `Search the web for real-time information using Tavily API.
Use this tool when you need up-to-date information that might not be in your training data.
Parameters (JSON):
  - query: string, the search query (required)
  - search_depth: string, "basic" or "advanced" (optional, default: "basic")
  - max_results: number, maximum number of results to return (optional, default: 5, max: 10)
  - include_answer: boolean, whether to include an AI-generated answer (optional, default: true)
Example: {"query": "latest news about AI", "max_results": 5}`
}

func (t *WebSearchTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "query", Type: "string", Description: "The search query to look up on the web", Required: true},
		{Name: "search_depth", Type: "string", Description: "Search depth: 'basic' or 'advanced'", Required: false},
		{Name: "max_results", Type: "number", Description: "Maximum number of results (1-10)", Required: false},
		{Name: "include_answer", Type: "boolean", Description: "Include AI-generated answer summary", Required: false},
	}
}

// TavilySearchRequest Tavily search request
type TavilySearchRequest struct {
	Query         string `json:"query"`
	SearchDepth   string `json:"search_depth,omitempty"`
	MaxResults    int    `json:"max_results,omitempty"`
	IncludeAnswer bool   `json:"include_answer,omitempty"`
}

// TavilySearchResult Tavily search result
type TavilySearchResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// TavilySearchResponse Tavily search response
type TavilySearchResponse struct {
	Query   string               `json:"query"`
	Answer  string               `json:"answer,omitempty"`
	Results []TavilySearchResult `json:"results"`
}

func (t *WebSearchTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	// Check API Key
	if t.apiKey == "" {
		return nil, fmt.Errorf("TAVILY_API_KEY environment variable is not set")
	}

	// Parse input parameters
	params, err := parseToolArgs[struct {
		Query         string `json:"query"`
		SearchDepth   string `json:"search_depth"`
		MaxResults    int    `json:"max_results"`
		IncludeAnswer *bool  `json:"include_answer"`
	}](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	// Set default values
	searchDepth := "basic"
	if params.SearchDepth == "advanced" {
		searchDepth = "advanced"
	}

	maxResults := 5
	if params.MaxResults > 0 && params.MaxResults <= 10 {
		maxResults = params.MaxResults
	}

	includeAnswer := true
	if params.IncludeAnswer != nil {
		includeAnswer = *params.IncludeAnswer
	}

	// Build request
	reqBody := TavilySearchRequest{
		Query:         params.Query,
		SearchDepth:   searchDepth,
		MaxResults:    maxResults,
		IncludeAnswer: includeAnswer,
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// sends request (supports context cancellation)
	req, err := http.NewRequestWithContext(ctx.Ctx, "POST", "https://api.tavily.com/search", bytes.NewBuffer(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.apiKey)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response (max 10MB limit to prevent abnormal responses from consuming too much memory)
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBodySize))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tavily API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var searchResp TavilySearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Format output
	return NewResult(formatSearchResults(&searchResp)), nil
}

// formatSearchResults formats search results
func formatSearchResults(resp *TavilySearchResponse) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# Web Search Results for: %s\n\n", resp.Query)

	// if AI-generated answer exists, show it first
	if resp.Answer != "" {
		sb.WriteString("## Summary\n")
		sb.WriteString(resp.Answer)
		sb.WriteString("\n\n")
	}

	// Display search results
	if len(resp.Results) > 0 {
		sb.WriteString("## Sources\n\n")
		for i, result := range resp.Results {
			fmt.Fprintf(&sb, "### %d. %s\n", i+1, result.Title)
			fmt.Fprintf(&sb, "**URL:** %s\n\n", result.URL)
			if result.Content != "" {
				sb.WriteString(result.Content)
				sb.WriteString("\n\n")
			}
		}
	} else {
		sb.WriteString("No results found.\n")
	}

	return sb.String()
}
