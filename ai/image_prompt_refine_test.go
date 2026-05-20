package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"openxhh/config"
	"openxhh/loger"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestParseImagePromptRefineContent(t *testing.T) {
	content := "```json\n{\"image_prompt\":\" 一只机械朋克猫 \" , \"mention_target\":\" 小菲 \", \"needs_post_context\":true, \"needs_comment_context\":false, \"needs_image_input\":false}\n```"

	got, err := ParseImagePromptRefineContent(content)
	if err != nil {
		t.Fatalf("ParseImagePromptRefineContent returned error: %v", err)
	}
	if got.ImagePrompt != "一只机械朋克猫" {
		t.Fatalf("ImagePrompt = %q", got.ImagePrompt)
	}
	if got.MentionTarget != "小菲" {
		t.Fatalf("MentionTarget = %q", got.MentionTarget)
	}
	if !got.NeedsPostContext {
		t.Fatal("NeedsPostContext should be true")
	}
}

func TestParseImagePromptRefineContentEmptyPrompt(t *testing.T) {
	_, err := ParseImagePromptRefineContent(`{"image_prompt":""}`)
	if err == nil {
		t.Fatal("expected error for empty image_prompt")
	}
}

func TestRefineImagePromptUsesResponsesInput(t *testing.T) {
	oldImage := config.ConfigStruct.Image
	oldLogger := loger.Loger
	t.Cleanup(func() {
		config.ConfigStruct.Image = oldImage
		loger.Loger = oldLogger
	})
	loger.Loger = zap.NewNop()

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
		_, _ = w.Write([]byte(`{"output_text":"{\"image_prompt\":\"一只猫娘海报\",\"mention_target\":\"\",\"needs_post_context\":false,\"needs_comment_context\":false,\"needs_image_input\":true}","usage":{"total_tokens":5}}`))
	}))
	defer server.Close()

	config.ConfigStruct.Image.PromptRefine = true
	config.ConfigStruct.Image.PromptModel = "prompt-model"
	config.ConfigStruct.Image.PromptBaseUrl = server.URL + "/v1/responses"

	got, err := RefineImagePrompt(context.Background(), ImagePromptRefineRequest{
		OriginalText:  "根据这张图生成海报",
		RulePrompt:    "根据参考图片生成类似图片",
		ContextPrompt: "参考图是一只猫",
		UseImageInput: true,
	})
	if err != nil {
		t.Fatalf("RefineImagePrompt returned error: %v", err)
	}
	if got.ImagePrompt != "一只猫娘海报" || !got.NeedsImageInput {
		t.Fatalf("result = %+v", got)
	}

	result := <-resultCh
	if result.err != nil {
		t.Fatalf("decode request body: %v", result.err)
	}
	if result.body.Model != "prompt-model" {
		t.Fatalf("model = %q", result.body.Model)
	}
	if len(result.body.Input) != 2 || result.body.Input[0].Role != "developer" || result.body.Input[1].Role != "user" {
		t.Fatalf("input roles = %+v", result.body.Input)
	}
	if len(result.body.Input[1].Content) != 1 || result.body.Input[1].Content[0].Type != "input_text" || !strings.Contains(result.body.Input[1].Content[0].Text, "根据这张图生成海报") {
		t.Fatalf("user content = %+v", result.body.Input[1].Content)
	}
}

func TestLimitRefineRunes(t *testing.T) {
	got := limitRefineRunes("猫猫猫", 2)
	if got != "猫猫" {
		t.Fatalf("limitRefineRunes = %q", got)
	}
}

func TestImagePromptRefineSystemPromptGuidesContextUnderstanding(t *testing.T) {
	prompt := imagePromptRefineSystemPrompt()
	for _, want := range []string{"主体必须优先来自对应上下文", "mention_target", "唤醒标记", "不要覆盖上下文主体"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("imagePromptRefineSystemPrompt should contain %q", want)
		}
	}
}
