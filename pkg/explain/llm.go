package explain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

// ClaudeModel and GeminiModel are the models --llm uses.
var (
	ClaudeModel = "claude-opus-5-5"
	GeminiModel = "gemini-2.5-flash"
)

const llmInstructions = `You are explaining a failed distributed-systems test to an engineer.
Below are the facts from one Kurukshetra run (faults injected, leader changes, the operations a
checker blamed) and a rule-based draft explanation. Rewrite the draft as a short root-cause story
in plain English Markdown, at most about 200 words: what fault happened, who was leader, which
operation broke which guarantee (linearizability or durability), and why the system allowed it.
Keep every step number and node name exactly as given. Do not invent facts that are not below.`

// Provider names which LLM --llm would use, from the environment:
// ANTHROPIC_API_KEY → "claude", else GEMINI_API_KEY → "gemini", else "".
func Provider() string {
	switch {
	case os.Getenv("ANTHROPIC_API_KEY") != "":
		return "claude"
	case os.Getenv("GEMINI_API_KEY") != "":
		return "gemini"
	}
	return ""
}

// LLMStory sends the facts and the rule-based draft to Claude or Gemini and
// returns the rewritten story.
func LLMStory(ctx context.Context, f Facts, draft string) (string, error) {
	facts, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	prompt := llmInstructions + "\n\nFACTS (JSON):\n" + string(facts) + "\n\nDRAFT:\n" + draft
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	switch Provider() {
	case "claude":
		return claudeStory(ctx, prompt)
	case "gemini":
		return geminiStory(ctx, prompt)
	}
	return "", errors.New("--llm needs ANTHROPIC_API_KEY or GEMINI_API_KEY")
}

func claudeStory(ctx context.Context, prompt string) (string, error) {
	client := anthropic.NewClient() // reads ANTHROPIC_API_KEY
	resp, err := client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:        ClaudeModel,
		MaxTokens:    16000,
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium},
		// On a safety refusal the server retries on a suitable fallback model.
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(prompt))},
	})
	if err != nil {
		return "", fmt.Errorf("claude: %w", err)
	}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return "", errors.New("claude declined to explain this run")
	}
	var out strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			out.WriteString(t.Text)
		}
	}
	if out.Len() == 0 {
		return "", errors.New("claude returned no text")
	}
	return strings.TrimSpace(out.String()) + "\n", nil
}

func geminiStory(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"contents": []any{map[string]any{"parts": []any{map[string]string{"text": prompt}}}},
	})
	url := "https://generativelanguage.googleapis.com/v1beta/models/" + GeminiModel + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", os.Getenv("GEMINI_API_KEY"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var r struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("gemini: %w", err)
	}
	var out strings.Builder
	for _, c := range r.Candidates {
		for _, p := range c.Content.Parts {
			out.WriteString(p.Text)
		}
	}
	if out.Len() == 0 {
		return "", errors.New("gemini returned no text")
	}
	return strings.TrimSpace(out.String()) + "\n", nil
}
