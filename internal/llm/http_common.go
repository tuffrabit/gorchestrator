package llm

import (
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/genai"
)

// Shared helpers for the hand-rolled HTTP model adapters (Anthropic). The
// OpenAI provider no longer uses these: it is backed by ADK's openai-go
// plugin (openai.go), which does its own retries.

func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

func retryAfter(header string, fallback time.Duration) time.Duration {
	if header == "" {
		return fallback
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(header); err == nil {
		return time.Until(t)
	}
	return fallback
}

func addJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	jitter := time.Duration(rand.Int63n(int64(d)))
	return d + jitter/2
}

func textFromContent(c *genai.Content) string {
	if c == nil {
		return ""
	}
	var parts []string
	for _, p := range c.Parts {
		if p != nil && p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return joinStrings(parts, "\n")
}

func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for i := 1; i < len(parts); i++ {
		out += sep + parts[i]
	}
	return out
}
