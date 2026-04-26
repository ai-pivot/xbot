package tools

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/go-shiori/go-readability"
	"github.com/tiktoken-go/tokenizer"
	"xbot/llm"
)

// FetchTool web page fetching tool
type FetchTool struct {
	// Note: httpClient removed — each request now creates a fresh transport with DNS rebinding protection.
	tokenizer tokenizer.Codec
}

// NewFetchTool creates a FetchTool
func NewFetchTool() *FetchTool {
	// Create tokenizer (reuse)
	enc, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		slog.Warn("Failed to initialize tokenizer, token counting will use rough estimation", "error", err)
	}

	return &FetchTool{
		tokenizer: enc,
	}
}

func (t *FetchTool) Name() string {
	return "Fetch"
}

func (t *FetchTool) Description() string {
	return `Fetch a webpage and convert it to LLM-friendly Markdown format.
Use this tool when you need to extract content from a URL.
Parameters (JSON):
  - url: string, the URL to fetch (required)
  - max_tokens: number, maximum output tokens (optional, default: 4096, max: 30000)
Example: {"url": "https://example.com", "max_tokens": 5000}`
}

func (t *FetchTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "url", Type: "string", Description: "The URL to fetch", Required: true},
		{Name: "max_tokens", Type: "number", Description: "Maximum output tokens (default: 4096, max: 30000)", Required: false},
	}
}

// fetchParams: Fetch tool parameters
type fetchParams struct {
	URL       string `json:"url"`
	MaxTokens int    `json:"max_tokens"`
}

func (t *FetchTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	// Parse parameters
	params, err := parseToolArgs[fetchParams](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.URL == "" {
		return nil, fmt.Errorf("url is required")
	}

	// Validate URL
	if err := validateURL(params.URL); err != nil {
		return nil, err
	}

	// Set default max_tokens
	if params.MaxTokens <= 0 {
		params.MaxTokens = 4096
	}
	if params.MaxTokens > 30000 {
		params.MaxTokens = 30000
	}

	// make HTTP request
	resp, err := t.fetchURL(ctx, params.URL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Check Content-Type
	contentType := resp.Header.Get("Content-Type")

	// Read response (max 10MB limit)
	reader := io.LimitedReader{R: resp.Body, N: maxHTTPResponseBodySize}
	htmlContent, err := io.ReadAll(&reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// text/plain (e.g. GitHub raw files) return content as-is
	if strings.Contains(contentType, "text/plain") {
		content := string(htmlContent)
		// Format output consistent with HTML (with URL and separator)
		content = t.formatAsPlainText(content, params.URL)
		content, _ = t.truncateByTokens(content, params.MaxTokens)
		return NewResult(content), nil
	}

	// supports text/html and application/xhtml+xml
	isHTML := strings.Contains(contentType, "text/html") ||
		strings.Contains(contentType, "application/xhtml+xml")
	if !isHTML {
		return nil, fmt.Errorf("unsupported content type: %s", contentType)
	}

	// extracts body using go-readability
	parsedURL, err := url.Parse(params.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}
	article, err := readability.FromReader(strings.NewReader(string(htmlContent)), parsedURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse webpage: %w", err)
	}

	// Build Markdown content
	content := t.formatAsMarkdown(&article, params.URL)

	// Token truncation
	content, _ = t.truncateByTokens(content, params.MaxTokens)

	// Build output
	return NewResult(content), nil
}

// fetchURL: fetch URL content
// uses a custom dialer to prevent DNS rebinding: verifies target IP is not private on TCP connect.
func (t *FetchTool) fetchURL(ctx *ToolContext, targetURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx.Ctx, "GET", targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set a reasonable User-Agent
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; xbot/1.0; +https://github.com/CjiW/xbot)")

	// don't send Authorization header
	req.Header.Del("Authorization")

	// creates a custom Transport that verifies IP is not private on dial (prevents DNS rebinding TOCTOU)
	safeTransport := &http.Transport{
		DialContext: func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("invalid address %q: %w", addr, err)
			}
			// Resolve IP, verify it's not a private address
			ips, err := net.DefaultResolver.LookupIPAddr(dialCtx, host)
			if err != nil {
				return nil, fmt.Errorf("DNS lookup failed for %q: %w", host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("no IP addresses found for %q", host)
			}
			// Select the first IPv4 or IPv6 address
			chosenIP := ips[0].IP
			for _, ipAddr := range ips {
				if v4 := ipAddr.IP.To4(); v4 != nil {
					chosenIP = v4
					break
				}
			}
			if isPrivateIPRaw(chosenIP) {
				return nil, fmt.Errorf("DNS rebinding protection: %q resolves to private IP %s", host, chosenIP)
			}
			dialer := &net.Dialer{}
			return dialer.DialContext(dialCtx, network, net.JoinHostPort(chosenIP.String(), port))
		},
	}

	client := &http.Client{
		Timeout:   httpDefaultTimeout,
		Transport: safeTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			// Also verify the redirect target is not a private address
			host := req.URL.Hostname()
			ips, err := net.DefaultResolver.LookupIPAddr(req.Context(), host)
			if err != nil {
				return fmt.Errorf("redirect DNS lookup failed for %s: %w", host, err)
			}
			for _, ipAddr := range ips {
				if isPrivateIPRaw(ipAddr.IP) {
					return fmt.Errorf("redirect to private IP %s blocked (DNS rebinding protection)", ipAddr.IP)
				}
			}
			return nil
		},
	}

	return client.Do(req)
}

// validateURL validates URL safety
func validateURL(rawURL string) error {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// protocol check
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("unsupported protocol: %s (only http and https are allowed)", parsedURL.Scheme)
	}

	host := parsedURL.Hostname()

	// domain check: localhost
	if host == "localhost" || host == "localhost.localdomain" {
		return fmt.Errorf("localhost is not allowed")
	}

	// private IP check
	if isPrivateIP(host) {
		return fmt.Errorf("private/internal IP addresses are not allowed: %s", host)
	}

	// S-02: DNS resolution check — prevent resolution to private IPs (DNS rebinding attack)
	ips, err := net.LookupIP(host)
	if err == nil {
		for _, ip := range ips {
			if isPrivateIPRaw(ip) {
				return fmt.Errorf("hostname %s resolves to private/internal IP %s (DNS rebinding protection)", host, ip)
			}
		}
	}

	return nil
}

// isPrivateIP checks if hostname is a private IP (literal IP only, no DNS resolution)
// S-03: Refactored to support IPv6 private address detection, internally delegates to isPrivateIPRaw
func isPrivateIP(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		// not a literal IP (may be a domain); DNS resolution check handled separately in validateURL
		return false
	}
	return isPrivateIPRaw(ip)
}

// isPrivateIPRaw checks if an IP address (IPv4 or IPv6) is private/internal
// S-03: Added IPv6 private address check (loopback, ULA, link-local, IPv4-mapped)
func isPrivateIPRaw(ip net.IP) bool {
	// IPv4-mapped IPv6 addresses (::ffff:x.x.x.x)
	if ip4 := ip.To4(); ip4 != nil {
		return isPrivateIPv4(ip4)
	}

	// native IPv6 check
	if ip.IsLoopback() { // ::1
		return true
	}

	// fc00::/7 — Unique Local Addresses (ULA/private)
	if len(ip) >= 1 && (ip[0]&0xfe) == 0xfc {
		return true
	}

	// fe80::/10 — Link-Local Addresses
	if len(ip) >= 2 && ip[0] == 0xfe && (ip[1]&0xc0) == 0x80 {
		return true
	}

	return false
}

// isPrivateIPv4 checks if an IPv4 address is private/internal
func isPrivateIPv4(ipv4 net.IP) bool {
	// 127.0.0.0/8 (loopback)
	if ipv4.IsLoopback() {
		return true
	}
	// 10.0.0.0/8
	if ipv4[0] == 10 {
		return true
	}
	// 172.16.0.0/12
	if ipv4[0] == 172 && ipv4[1] >= 16 && ipv4[1] <= 31 {
		return true
	}
	// 192.168.0.0/16
	if ipv4[0] == 192 && ipv4[1] == 168 {
		return true
	}
	// 169.254.0.0/16 (link-local)
	if ipv4[0] == 169 && ipv4[1] == 254 {
		return true
	}
	// 0.0.0.0/8
	if ipv4[0] == 0 {
		return true
	}
	return false
}

// formatAsMarkdown: format an article as Markdown
func (t *FetchTool) formatAsMarkdown(article *readability.Article, pageURL string) string {
	var sb strings.Builder

	// Title
	title := strings.TrimSpace(article.Title)
	if title != "" {
		sb.WriteString("# ")
		sb.WriteString(title)
		sb.WriteString("\n\n")
	}

	// URL
	sb.WriteString("**URL:** ")
	sb.WriteString(pageURL)
	sb.WriteString("\n\n")
	sb.WriteString("---\n\n")

	// Body — convert HTML to Markdown format
	markdownContent := convertHTMLToMarkdown(article.Content, pageURL, article.TextContent)
	sb.WriteString(markdownContent)

	return sb.String()
}

// formatAsPlainText: format plain text as Markdown (consistent with HTML branch format)
func (t *FetchTool) formatAsPlainText(content, pageURL string) string {
	var sb strings.Builder

	// URL
	sb.WriteString("**URL:** ")
	sb.WriteString(pageURL)
	sb.WriteString("\n\n")
	sb.WriteString("---\n\n")

	// Body
	sb.WriteString(content)

	return sb.String()
}

// convertHTMLToMarkdown converts HTML content to Markdown format
func convertHTMLToMarkdown(htmlContent, pageURL string, fallbackText string) string {
	// if no HTML content, use fallback text
	if htmlContent == "" {
		return fallbackText
	}

	// Parse pageURL to get the domain, used for handling relative links
	u, err := url.Parse(pageURL)
	if err != nil {
		return fallbackText
	}

	// Use top-level function for conversion (supports WithDomain)
	markdown, err := htmltomarkdown.ConvertString(
		htmlContent,
		converter.WithDomain(u.Hostname()),
	)
	if err != nil {
		// If conversion fails, fall back to plain text
		return fallbackText
	}

	return strings.TrimSpace(markdown)
}

// truncateByTokens truncates content by token count, returns actual token count
func (t *FetchTool) truncateByTokens(content string, maxTokens int) (string, int) {
	// Use the tokenizer from the struct (already created during initialization)
	if t.tokenizer == nil {
		// if tokenizer not initialized, don't truncate
		return content, countTokensRoughly(content)
	}

	ids, _, err := t.tokenizer.Encode(content)
	if err != nil {
		// if failed, don't truncate
		return content, countTokensRoughly(content)
	}

	actualTokens := len(ids)

	// if within limit, return directly
	if actualTokens <= maxTokens {
		return content, actualTokens
	}

	// Truncate to maxTokens
	truncated := ids[:maxTokens]
	truncatedContent, err := t.tokenizer.Decode(truncated)
	if err != nil {
		// Truncation failed, don't truncate
		return content, actualTokens
	}

	// Add truncation notice
	var sb strings.Builder
	sb.WriteString(truncatedContent)
	fmt.Fprintf(&sb, "\n\n---\n\n*⚠️ 内容已截断（已截取 %d / %d tokens）*", maxTokens, actualTokens)

	return sb.String(), maxTokens
}

// countTokensRoughly roughly estimates token count (chars/4 is a common estimate)
func countTokensRoughly(content string) int {
	return len(content) / 4
}
