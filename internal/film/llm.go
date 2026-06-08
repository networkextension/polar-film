package film

// llm.go — thin client for the dock LLM proxy + JSON extraction helpers
// used by the M5 analyze pipeline. The dock owns provider auth, model
// resolution and billing; film just sends messages and reads `content`.
// No SDK helper exists yet, so we sign through p.Dock.Do (HMAC).
// Contract: POST /internal/v1/llm/chat-completion (internal-api-v1.md).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type llmMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type llmChatReq struct {
	WorkspaceID string       `json:"workspace_id"`
	LLMConfigID int64        `json:"llm_config_id"`
	Messages    []llmMessage `json:"messages"`
	MaxTokens   int          `json:"max_tokens"`
	EndpointTag string       `json:"endpoint_tag"`
}

type llmChatResp struct {
	Content     string `json:"content"`
	Model       string `json:"model"`
	TotalTokens int    `json:"total_tokens"`
}

// errNoLLMConfig signals no llm_config_id was supplied — the caller should
// degrade (skip the AI step) rather than fail.
var errNoLLMConfig = errors.New("no llm_config_id provided")

// llmComplete runs a system+user chat completion against a workspace LLM
// config and returns the generated text. cfgID <= 0 → errNoLLMConfig.
func (p *Plugin) llmComplete(ctx context.Context, wsID string, cfgID int64, system, user, tag string, maxTokens int) (string, error) {
	if cfgID <= 0 {
		return "", errNoLLMConfig
	}
	if maxTokens <= 0 {
		maxTokens = 512
	}
	if tag == "" {
		tag = "film.analyze"
	}
	body := llmChatReq{
		WorkspaceID: wsID,
		LLMConfigID: cfgID,
		Messages: []llmMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		MaxTokens:   maxTokens,
		EndpointTag: tag,
	}
	resp, err := p.dockLLM.Do(http.MethodPost, "/internal/v1/llm/chat-completion", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
			Kind  string `json:"kind"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return "", fmt.Errorf("llm chat-completion HTTP %d: %s%s", resp.StatusCode, e.Error, kindSuffix(e.Kind))
	}
	var out llmChatResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	text := strings.TrimSpace(out.Content)
	if text == "" {
		return "", errors.New("llm returned empty content")
	}
	return text, nil
}

func kindSuffix(kind string) string {
	if kind == "" {
		return ""
	}
	return " (" + kind + ")"
}

// extractJSONArray pulls the first top-level JSON array out of an LLM reply,
// tolerating ```json fences and surrounding prose. Returns "[]" if none.
func extractJSONArray(s string) string {
	s = stripCodeFences(s)
	start := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if start < 0 || end <= start {
		return "[]"
	}
	return s[start : end+1]
}

// stripCodeFences removes a leading/trailing ``` or ```json fence if present.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// drop first line (``` or ```json) and a trailing ```
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}

func parseTagList(s string) []string {
	var raw []string
	if err := json.Unmarshal([]byte(extractJSONArray(s)), &raw); err != nil {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, t := range raw {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

type timelineBeat struct {
	StartMs     int    `json:"start_ms"`
	EventType   string `json:"event_type"`
	Description string `json:"description"`
}

func parseTimelineBeats(s string) []timelineBeat {
	var beats []timelineBeat
	if err := json.Unmarshal([]byte(extractJSONArray(s)), &beats); err != nil {
		return nil
	}
	out := beats[:0]
	for _, b := range beats {
		if strings.TrimSpace(b.Description) != "" {
			out = append(out, b)
		}
	}
	return out
}
