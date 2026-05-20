package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"openxhh/config"
	"openxhh/loger"
	"strings"
	"time"

	"go.uber.org/zap"
)

type ImagePromptRefineRequest struct {
	OriginalText      string
	RulePrompt        string
	ContextPrompt     string
	UsePostContext    bool
	UseCommentContext bool
	UseImageInput     bool
}

type ImagePromptRefineResult struct {
	ImagePrompt         string `json:"image_prompt"`
	MentionTarget       string `json:"mention_target"`
	NeedsPostContext    bool   `json:"needs_post_context"`
	NeedsCommentContext bool   `json:"needs_comment_context"`
	NeedsImageInput     bool   `json:"needs_image_input"`
}

type promptRefineMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type promptRefineBody struct {
	Model    string                `json:"model"`
	Messages []promptRefineMessage `json:"messages"`
	Stream   bool                  `json:"stream"`
}

type promptRefineResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func ShouldRefineImagePrompt() bool {
	cfg := config.ConfigStruct.Image
	return cfg.PromptRefine && strings.TrimSpace(promptRefineBaseURL()) != "" && strings.TrimSpace(promptRefineModel()) != ""
}

func RefineImagePrompt(ctx context.Context, req ImagePromptRefineRequest) (ImagePromptRefineResult, error) {
	if !ShouldRefineImagePrompt() {
		return ImagePromptRefineResult{}, errors.New("image prompt refine is not configured")
	}

	useResponses := useResponsesAPI(promptRefineBaseURL())
	payload, err := buildImagePromptRefinePayload(req, useResponses)
	if err != nil {
		return ImagePromptRefineResult{}, err
	}

	started := time.Now()
	var lastErr error
	for attempt := 1; attempt <= chatCompletionAttempts; attempt++ {
		result, err := refineImagePromptOnce(ctx, payload, useResponses)
		if err == nil {
			loger.Loger.Info("[Image]文本模型已优化生图 prompt", zap.Int("prompt_chars", len([]rune(result.ImagePrompt))), zap.Duration("duration", time.Since(started)))
			return result, nil
		}
		lastErr = err
		if !shouldRetryChatCompletionError(err) || attempt == chatCompletionAttempts {
			return ImagePromptRefineResult{}, fmt.Errorf("prompt refine request failed after %s: %w", time.Since(started).Round(time.Second), err)
		}
		if err := waitForChatCompletionRetry(ctx, attempt); err != nil {
			return ImagePromptRefineResult{}, err
		}
	}
	return ImagePromptRefineResult{}, lastErr
}

func buildImagePromptRefinePayload(req ImagePromptRefineRequest, useResponses bool) ([]byte, error) {
	messages := []promptRefineMessage{
		{Role: "system", Content: imagePromptRefineSystemPrompt()},
		{Role: "user", Content: buildImagePromptRefineUserPrompt(req)},
	}
	if useResponses {
		rawMessages := make([]any, 0, len(messages))
		for _, message := range messages {
			rawMessages = append(rawMessages, message)
		}
		input, err := toResponsesInput(rawMessages)
		if err != nil {
			return nil, err
		}
		body := responsesBodyStruct{
			Model:  promptRefineModel(),
			Input:  input,
			Stream: false,
		}
		return json.Marshal(body)
	}

	body := promptRefineBody{
		Model:    promptRefineModel(),
		Stream:   false,
		Messages: messages,
	}
	return json.Marshal(body)
}

func refineImagePromptOnce(ctx context.Context, payload []byte, useResponses bool) (ImagePromptRefineResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", promptRefineBaseURL(), bytes.NewReader(payload))
	if err != nil {
		return ImagePromptRefineResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token := promptRefineToken(); token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return ImagePromptRefineResult{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return ImagePromptRefineResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ImagePromptRefineResult{}, chatCompletionStatusError{statusCode: resp.StatusCode, body: limitRefineString(string(data), 300)}
	}

	if useResponses {
		parsed, err := parseResponsesResp(data)
		if err != nil {
			return ImagePromptRefineResult{}, err
		}
		if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Msg.Content) == "" {
			return ImagePromptRefineResult{}, errors.New("prompt refine response has no content")
		}
		return ParseImagePromptRefineContent(parsed.Choices[0].Msg.Content)
	}

	var parsed promptRefineResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ImagePromptRefineResult{}, err
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return ImagePromptRefineResult{}, errors.New("prompt refine response has no content")
	}
	return ParseImagePromptRefineContent(parsed.Choices[0].Message.Content)
}

func ParseImagePromptRefineContent(content string) (ImagePromptRefineResult, error) {
	jsonText := extractJSONText(content)
	var result ImagePromptRefineResult
	if err := json.Unmarshal([]byte(jsonText), &result); err != nil {
		return ImagePromptRefineResult{}, err
	}
	result.ImagePrompt = strings.TrimSpace(result.ImagePrompt)
	result.MentionTarget = strings.TrimSpace(result.MentionTarget)
	if result.ImagePrompt == "" {
		return ImagePromptRefineResult{}, errors.New("prompt refine returned empty image_prompt")
	}
	return result, nil
}

func imagePromptRefineSystemPrompt() string {
	return `你是 Openxhh 的生图 Prompt 优化器，只输出 JSON，不要 Markdown，不要解释。
你的任务不是复述用户评论，而是把用户的生图请求拆成：
1. image_prompt：给图片生成模型的最终画面描述
2. mention_target：用户想艾特、喊谁来看或回复谁
3. needs_post_context / needs_comment_context / needs_image_input

理解优先级：
1. 如果用户说“根据正文/文章/帖子/原帖/评论区/这层楼/这条评论/楼上内容/这张图片”，image_prompt 的主体必须优先来自对应上下文。
2. 用户后续的祝福、吐槽、夸奖、安慰、整活、嘲讽、可爱一点等短句，只作为画面情绪、立场、用途或风格，不要覆盖上下文主体。
3. “艾特谁来看、喊谁、回复谁、让谁看看”是评论控制指令，不得进入 image_prompt，只提取到 mention_target。
4. 机器人 @ 可能出现在开头、中间或结尾，只是唤醒标记，不是语义切分点。
5. 删除 @、HTML、平台表情标记如 [cube_喜欢]、上传/评论/回复等非画面内容。
6. image_prompt 必须是具体画面描述，包含主体、场景、氛围、风格、构图或色彩。
7. 默认不要出现文字、水印、二维码、平台 UI，除非用户明确要求。`
}

func buildImagePromptRefineUserPrompt(req ImagePromptRefineRequest) string {
	maxChars := config.ConfigStruct.Image.PromptMaxChars
	if maxChars <= 0 {
		maxChars = 3000
	}
	contextPrompt := limitRefineRunes(req.ContextPrompt, maxChars)
	return fmt.Sprintf(`请先判断用户要参考的内容范围，再根据上下文提炼画面主体。

原始评论：%s
规则提取的图片要求：%s
上下文内容：%s
当前标记：needs_post_context=%v, needs_comment_context=%v, needs_image_input=%v

输出要求：
- image_prompt：给图片生成模型使用的最终提示词。它必须是画面描述，而不是把“根据文章内容”“根据这层楼”“根据这张图片”“生成一张图片”“艾特小菲来看”这类控制指令原样搬进去。
- 如果存在上下文，主体必须优先来自上下文；用户评论里的短句只作为情绪、风格、用途或立场，不要覆盖上下文主体。
- mention_target：只填写用户明确要艾特、喊谁来看或回复谁的人名；没有就留空。
- 机器人 @ 可能出现在开头、中间或结尾，只表示唤醒，不是语义切分点。
- 不要把 HTML、@、平台表情或评论控制词写进 image_prompt。

只输出 JSON：{"image_prompt":"...","mention_target":"","needs_post_context":false,"needs_comment_context":false,"needs_image_input":false}`,
		req.OriginalText,
		req.RulePrompt,
		contextPrompt,
		req.UsePostContext,
		req.UseCommentContext,
		req.UseImageInput,
	)
}

func promptRefineModel() string {
	if config.ConfigStruct.Image.PromptModel != "" {
		return config.ConfigStruct.Image.PromptModel
	}
	return config.ConfigStruct.Ai.Model
}

func promptRefineBaseURL() string {
	if config.ConfigStruct.Image.PromptBaseUrl != "" {
		return config.ConfigStruct.Image.PromptBaseUrl
	}
	return config.ConfigStruct.Ai.BaseUrl
}

func promptRefineToken() string {
	if config.ConfigStruct.Image.PromptToken != "" {
		return config.ConfigStruct.Image.PromptToken
	}
	return config.ConfigStruct.Ai.Token
}

func extractJSONText(content string) string {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end >= start {
		return content[start : end+1]
	}
	return content
}

func limitRefineRunes(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return strings.TrimSpace(string(runes[:max]))
}

func limitRefineString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
