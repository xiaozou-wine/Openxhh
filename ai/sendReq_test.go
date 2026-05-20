package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"openxhh/config"
	"testing"
)

func TestBuildReqBodyAddsChatCompletionsWebSearchByDefault(t *testing.T) {
	restoreAIConfig(t)
	config.ConfigStruct.Ai.BaseUrl = "https://example.com/v1/chat/completions"
	config.ConfigStruct.Ai.WebSearch = nil
	config.ConfigStruct.Ai.SearchContextSize = ""

	body, err := buildReqBody("test-model", []any{Messages[string]{Role: "system", Content: "system prompt"}})
	if err != nil {
		t.Fatalf("buildReqBody returned error: %v", err)
	}

	var got struct {
		Model            string `json:"model"`
		WebSearchOptions struct {
			SearchContextSize string `json:"search_context_size"`
		} `json:"web_search_options"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("request body json = %s, error: %v", string(body), err)
	}
	if got.Model != "test-model" {
		t.Fatalf("model = %q", got.Model)
	}
	if got.WebSearchOptions.SearchContextSize != "medium" {
		t.Fatalf("search_context_size = %q, want medium", got.WebSearchOptions.SearchContextSize)
	}
}

func TestBuildReqBodyOmitsChatCompletionsWebSearchWhenDisabled(t *testing.T) {
	restoreAIConfig(t)
	config.ConfigStruct.Ai.BaseUrl = "https://example.com/v1/chat/completions"
	config.ConfigStruct.Ai.WebSearch = testBoolPtr(false)

	body, err := buildReqBody("test-model", []any{Messages[string]{Role: "system", Content: "system prompt"}})
	if err != nil {
		t.Fatalf("buildReqBody returned error: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("request body json = %s, error: %v", string(body), err)
	}
	if _, ok := got["web_search_options"]; ok {
		t.Fatalf("web_search_options should be omitted when webSearch is false: %s", string(body))
	}
}

func TestBuildReqBodyResponsesWebSearch(t *testing.T) {
	restoreAIConfig(t)
	config.ConfigStruct.Ai.BaseUrl = "https://example.com/v1/responses"
	config.ConfigStruct.Ai.WebSearch = testBoolPtr(true)
	config.ConfigStruct.Ai.ForceWebSearch = testBoolPtr(true)
	config.ConfigStruct.Ai.SearchContextSize = "high"

	image := Content{Type: "image_url"}
	image.ImgUrl.Url = "https://example.com/image.png"
	body, err := buildReqBody("test-model", []any{
		Messages[string]{Role: "system", Content: "system prompt"},
		Messages[[]Content]{Role: "user", Content: []Content{{Type: "text", Text: "hello"}, image}},
	})
	if err != nil {
		t.Fatalf("buildReqBody returned error: %v", err)
	}

	var got struct {
		Model string `json:"model"`
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL string `json:"image_url"`
			} `json:"content"`
		} `json:"input"`
		Tools []struct {
			Type              string `json:"type"`
			SearchContextSize string `json:"search_context_size"`
		} `json:"tools"`
		ToolChoice string `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("request body json = %s, error: %v", string(body), err)
	}
	if got.Model != "test-model" {
		t.Fatalf("model = %q", got.Model)
	}
	if len(got.Input) != 2 || got.Input[0].Role != "developer" || got.Input[1].Role != "user" {
		t.Fatalf("input roles = %+v", got.Input)
	}
	if len(got.Input[1].Content) != 2 || got.Input[1].Content[0].Type != "input_text" || got.Input[1].Content[1].Type != "input_image" {
		t.Fatalf("input content = %+v", got.Input[1].Content)
	}
	if got.Input[1].Content[1].ImageURL != "https://example.com/image.png" {
		t.Fatalf("image_url = %q", got.Input[1].Content[1].ImageURL)
	}
	if len(got.Tools) != 1 || got.Tools[0].Type != "web_search" || got.Tools[0].SearchContextSize != "high" {
		t.Fatalf("tools = %+v", got.Tools)
	}
	if got.ToolChoice != "required" {
		t.Fatalf("tool_choice = %q", got.ToolChoice)
	}
}

func TestParseResponsesResp(t *testing.T) {
	resp, err := parseResponsesResp([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"hello"},{"type":"output_text","text":"world"}]}],"usage":{"total_tokens":42}}`))
	if err != nil {
		t.Fatalf("parseResponsesResp returned error: %v", err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Msg.Content != "hello\nworld" {
		t.Fatalf("content = %+v", resp.Choices)
	}
	if resp.Usage.TotalToken != 42 {
		t.Fatalf("total_tokens = %d", resp.Usage.TotalToken)
	}

	resp, err = parseResponsesResp([]byte(`{"output_text":"direct","usage":{"total_tokens":7}}`))
	if err != nil {
		t.Fatalf("parseResponsesResp output_text returned error: %v", err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Msg.Content != "direct" || resp.Usage.TotalToken != 7 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestSendChatCompletionUsesResponsesInput(t *testing.T) {
	restoreAIConfig(t)

	type requestBody struct {
		Model string `json:"model"`
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
		Stream bool `json:"stream"`
	}
	type requestResult struct {
		body requestBody
		err  error
	}

	resultCh := make(chan requestResult, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body requestBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			resultCh <- requestResult{err: err}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		resultCh <- requestResult{body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"route-ok","usage":{"total_tokens":3}}`))
	}))
	defer server.Close()

	config.ConfigStruct.Ai.BaseUrl = server.URL + "/v1/responses"
	got, err := sendChatCompletion(context.Background(), "route-model", []chatCompletionMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "hello"},
	})
	if err != nil {
		t.Fatalf("sendChatCompletion returned error: %v", err)
	}
	if got != "route-ok" {
		t.Fatalf("response text = %q, want route-ok", got)
	}

	result := <-resultCh
	if result.err != nil {
		t.Fatalf("decode request body: %v", result.err)
	}
	if result.body.Model != "route-model" {
		t.Fatalf("model = %q", result.body.Model)
	}
	if len(result.body.Input) != 2 || result.body.Input[0].Role != "developer" || result.body.Input[1].Role != "user" {
		t.Fatalf("input roles = %+v", result.body.Input)
	}
	if len(result.body.Input[0].Content) != 1 || result.body.Input[0].Content[0].Type != "input_text" || result.body.Input[0].Content[0].Text != "system prompt" {
		t.Fatalf("developer content = %+v", result.body.Input[0].Content)
	}
	if len(result.body.Input[1].Content) != 1 || result.body.Input[1].Content[0].Type != "input_text" || result.body.Input[1].Content[0].Text != "hello" {
		t.Fatalf("user content = %+v", result.body.Input[1].Content)
	}
}

func restoreAIConfig(t *testing.T) {
	t.Helper()
	oldModel := config.ConfigStruct.Ai.Model
	oldPrompt := config.ConfigStruct.Ai.Prompt
	oldBaseURL := config.ConfigStruct.Ai.BaseUrl
	oldToken := config.ConfigStruct.Ai.Token
	oldWebSearch := config.ConfigStruct.Ai.WebSearch
	oldForceWebSearch := config.ConfigStruct.Ai.ForceWebSearch
	oldSearchContextSize := config.ConfigStruct.Ai.SearchContextSize
	t.Cleanup(func() {
		config.ConfigStruct.Ai.Model = oldModel
		config.ConfigStruct.Ai.Prompt = oldPrompt
		config.ConfigStruct.Ai.BaseUrl = oldBaseURL
		config.ConfigStruct.Ai.Token = oldToken
		config.ConfigStruct.Ai.WebSearch = oldWebSearch
		config.ConfigStruct.Ai.ForceWebSearch = oldForceWebSearch
		config.ConfigStruct.Ai.SearchContextSize = oldSearchContextSize
	})
}

func testBoolPtr(v bool) *bool {
	return &v
}
