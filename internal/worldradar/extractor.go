package worldradar

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

const maxExtractHTMLBytes = 2 << 20 // 2 MiB

// ErrSSRFBlocked is returned when a target URL resolves to a restricted/private network address.
var ErrSSRFBlocked = errors.New("target URL blocked by SSRF policy")

// ExtractMarkdownResult holds the extracted Markdown document and metadata.
type ExtractMarkdownResult struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Markdown  string `json:"markdown"`
	CharCount int    `json:"charCount"`
}

var (
	htmlCommentRegex     = regexp.MustCompile(`(?is)<!--.*?-->`)
	htmlDropBlockRegex   = regexp.MustCompile(`(?is)<(script|style|nav|footer|noscript|svg)\b[^>]*>.*?</(script|style|nav|footer|noscript|svg)>`)
	htmlTitleTagRegex    = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
	htmlH1Regex          = regexp.MustCompile(`(?is)<h1\b[^>]*>(.*?)</h1>`)
	htmlH2Regex          = regexp.MustCompile(`(?is)<h2\b[^>]*>(.*?)</h2>`)
	htmlH3Regex          = regexp.MustCompile(`(?is)<h3\b[^>]*>(.*?)</h3>`)
	htmlAnchorRegex      = regexp.MustCompile(`(?is)<a\b[^>]*\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))[^>]*>(.*?)</a>`)
	htmlListItemRegex    = regexp.MustCompile(`(?is)<li\b[^>]*>(.*?)</li>`)
	htmlBlockBreakRegex  = regexp.MustCompile(`(?is)</?(?:p|div|section|article|main|header|blockquote|ul|ol|tr|table|hr)\b[^>]*>`)
	htmlLineBreakRegex   = regexp.MustCompile(`(?is)<br\s*/?>`)
	htmlRemainingTagRe   = regexp.MustCompile(`(?is)<[^>]+>`)
	markdownBlankLineRe  = regexp.MustCompile(`\n{3,}`)
	markdownMultiSpaceRe = regexp.MustCompile(`[ \t]{2,}`)
)

// ExtractTitleFromHTML returns the document title from <title> or the first <h1>.
func ExtractTitleFromHTML(rawHTML string) string {
	if match := htmlTitleTagRegex.FindStringSubmatch(rawHTML); len(match) >= 2 {
		if cleaned := cleanInlineHTMLText(match[1]); cleaned != "" {
			return cleaned
		}
	}
	if match := htmlH1Regex.FindStringSubmatch(rawHTML); len(match) >= 2 {
		if cleaned := cleanInlineHTMLText(match[1]); cleaned != "" {
			return cleaned
		}
	}
	return ""
}

// ExtractMarkdownFromHTML strips non-content chrome (<script>, <style>, <nav>, <footer>)
// and converts headings (h1..h3), list items (li), links (a href), and paragraphs into Markdown.
func ExtractMarkdownFromHTML(rawHTML, baseURL string) string {
	if strings.TrimSpace(rawHTML) == "" {
		return ""
	}

	var parsedBase *url.URL
	if trimmedBase := strings.TrimSpace(baseURL); trimmedBase != "" {
		if u, err := url.Parse(trimmedBase); err == nil {
			parsedBase = u
		}
	}

	cleaned := htmlCommentRegex.ReplaceAllString(rawHTML, "")
	cleaned = htmlDropBlockRegex.ReplaceAllString(cleaned, "\n")

	// Convert anchors first so links inside headings/list items/paragraphs are preserved.
	cleaned = htmlAnchorRegex.ReplaceAllStringFunc(cleaned, func(rawMatch string) string {
		parts := htmlAnchorRegex.FindStringSubmatch(rawMatch)
		if len(parts) < 5 {
			return ""
		}
		rawHref := firstNonEmpty(parts[1], parts[2], parts[3])
		linkText := cleanInlineHTMLText(parts[4])
		resolvedHref := resolveRelativeLink(rawHref, parsedBase)
		if linkText == "" {
			linkText = resolvedHref
		}
		if resolvedHref == "" {
			return linkText
		}
		return fmt.Sprintf("[%s](%s)", linkText, resolvedHref)
	})

	// Headings h1..h3
	cleaned = htmlH1Regex.ReplaceAllStringFunc(cleaned, func(rawMatch string) string {
		parts := htmlH1Regex.FindStringSubmatch(rawMatch)
		text := cleanInlineHTMLText(parts[1])
		if text == "" {
			return "\n"
		}
		return "\n\n# " + text + "\n\n"
	})
	cleaned = htmlH2Regex.ReplaceAllStringFunc(cleaned, func(rawMatch string) string {
		parts := htmlH2Regex.FindStringSubmatch(rawMatch)
		text := cleanInlineHTMLText(parts[1])
		if text == "" {
			return "\n"
		}
		return "\n\n## " + text + "\n\n"
	})
	cleaned = htmlH3Regex.ReplaceAllStringFunc(cleaned, func(rawMatch string) string {
		parts := htmlH3Regex.FindStringSubmatch(rawMatch)
		text := cleanInlineHTMLText(parts[1])
		if text == "" {
			return "\n"
		}
		return "\n\n### " + text + "\n\n"
	})

	// List items
	cleaned = htmlListItemRegex.ReplaceAllStringFunc(cleaned, func(rawMatch string) string {
		parts := htmlListItemRegex.FindStringSubmatch(rawMatch)
		text := cleanInlineHTMLText(parts[1])
		if text == "" {
			return "\n"
		}
		return "\n- " + text + "\n"
	})

	// Line breaks and block-level tags
	cleaned = htmlLineBreakRegex.ReplaceAllString(cleaned, "\n")
	cleaned = htmlBlockBreakRegex.ReplaceAllString(cleaned, "\n\n")
	cleaned = htmlRemainingTagRe.ReplaceAllString(cleaned, "")
	cleaned = html.UnescapeString(cleaned)

	rawLines := strings.Split(strings.ReplaceAll(cleaned, "\r\n", "\n"), "\n")
	outLines := make([]string, 0, len(rawLines))
	for _, rawLine := range rawLines {
		collapsed := strings.TrimSpace(markdownMultiSpaceRe.ReplaceAllString(rawLine, " "))
		outLines = append(outLines, collapsed)
	}
	joined := strings.Join(outLines, "\n")
	joined = markdownBlankLineRe.ReplaceAllString(joined, "\n\n")
	return strings.TrimSpace(joined)
}

func cleanInlineHTMLText(fragment string) string {
	stripped := htmlRemainingTagRe.ReplaceAllString(fragment, " ")
	unescaped := html.UnescapeString(stripped)
	return strings.TrimSpace(markdownMultiSpaceRe.ReplaceAllString(strings.ReplaceAll(unescaped, "\n", " "), " "))
}

func resolveRelativeLink(rawHref string, base *url.URL) string {
	trimmed := strings.TrimSpace(html.UnescapeString(rawHref))
	if trimmed == "" || strings.HasPrefix(strings.ToLower(trimmed), "javascript:") {
		return ""
	}
	parsedHref, err := url.Parse(trimmed)
	if err != nil {
		return trimmed
	}
	if base != nil {
		return base.ResolveReference(parsedHref).String()
	}
	return parsedHref.String()
}

// ValidatePublicHTTPURL checks that targetURL uses http(s) and does not resolve
// to loopback, RFC1918 private, link-local, or unspecified IP ranges.
func ValidatePublicHTTPURL(ctx context.Context, targetURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return nil, fmt.Errorf("%w: valid http(s) url is required", ErrValidation)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, fmt.Errorf("%w: loopback host %q is forbidden", ErrSSRFBlocked, host)
	}
	if ipAddr, parseErr := netip.ParseAddr(host); parseErr == nil {
		if isBlockedSSRFAddr(ipAddr) {
			return nil, fmt.Errorf("%w: restricted IP %s", ErrSSRFBlocked, ipAddr.String())
		}
		return parsed, nil
	}

	resolvedAddrs, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, host)
	if lookupErr != nil || len(resolvedAddrs) == 0 {
		return nil, fmt.Errorf("%w: failed to resolve host %q", ErrValidation, host)
	}
	for _, resolved := range resolvedAddrs {
		if addr, ok := netip.AddrFromSlice(resolved.IP); ok {
			if isBlockedSSRFAddr(addr.Unmap()) {
				return nil, fmt.Errorf("%w: host %q resolved to restricted IP %s", ErrSSRFBlocked, host, addr.String())
			}
		}
	}
	return parsed, nil
}

func isBlockedSSRFAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified()
}

// FetchAndExtractMarkdown fetches a remote URL with SSRF protection (unless allowPrivateForTest is true)
// and returns its extracted Markdown representation.
func (e *Engine) FetchAndExtractMarkdown(ctx context.Context, targetURL string,
	allowPrivateForTest bool) (ExtractMarkdownResult, error) {
	trimmedURL := strings.TrimSpace(targetURL)
	if !allowPrivateForTest {
		if _, err := ValidatePublicHTTPURL(ctx, trimmedURL); err != nil {
			return ExtractMarkdownResult{}, err
		}
	} else {
		parsed, err := url.Parse(trimmedURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return ExtractMarkdownResult{}, fmt.Errorf("%w: valid http(s) url is required", ErrValidation)
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, trimmedURL, nil)
	if err != nil {
		return ExtractMarkdownResult{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	httpReq.Header.Set("User-Agent", "MW-Echo-WorldRadar/1.0 (+firecrawl-lite)")
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.8,*/*;q=0.5")

	httpResp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return ExtractMarkdownResult{}, fmt.Errorf("fetch url: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return ExtractMarkdownResult{}, fmt.Errorf("upstream HTTP %d", httpResp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, maxExtractHTMLBytes))
	if err != nil {
		return ExtractMarkdownResult{}, fmt.Errorf("read response body: %w", err)
	}

	rawHTML := string(bodyBytes)
	title := ExtractTitleFromHTML(rawHTML)
	markdown := ExtractMarkdownFromHTML(rawHTML, trimmedURL)
	return ExtractMarkdownResult{
		URL:       trimmedURL,
		Title:     title,
		Markdown:  markdown,
		CharCount: len(markdown),
	}, nil
}
