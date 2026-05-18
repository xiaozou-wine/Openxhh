package xhh

import (
	"strings"
	"xhhrobot/ai"
	"xhhrobot/loger"

	"go.uber.org/zap"
)

const imageContextMaxChars = 1200

func BuildContextualImagePrompt(basePrompt string, command ImageCommand, linkID int, rootID int, commentID int, userID int) string {
	if !command.UsePostContext && !command.UseCommentContext {
		return basePrompt
	}

	contents, _, _, _ := GetLinkInfo(linkID, rootID, commentID, userID)
	contextText := selectImageContextText(contents, command)
	if contextText == "" {
		return basePrompt
	}

	prompt := "请根据以下" + imageContextLabel(command) + "生成图片。\n" +
		"参考内容：" + contextText + "\n" +
		"图片要求：" + basePrompt
	loger.Loger.Info("[XHH]已构造上下文生图 prompt", zap.Int("context_chars", len(contextText)), zap.Bool("post_context", command.UsePostContext), zap.Bool("comment_context", command.UseCommentContext))
	return prompt
}

func selectImageContextText(contents []ai.Content, command ImageCommand) string {
	var parts []string
	for _, content := range contents {
		if content.Type != "text" || strings.TrimSpace(content.Text) == "" {
			continue
		}
		text := strings.TrimSpace(content.Text)
		if !command.UseCommentContext && strings.HasPrefix(text, "以下是评论区") {
			continue
		}
		parts = append(parts, text)
	}
	return limitImageContext(strings.Join(parts, "\n"), imageContextMaxChars)
}

func imageContextLabel(command ImageCommand) string {
	if command.UsePostContext && command.UseCommentContext {
		return "帖子正文和评论区内容"
	}
	if command.UseCommentContext {
		return "评论区内容"
	}
	return "帖子正文内容"
}

func limitImageContext(text string, maxChars int) string {
	text = strings.TrimSpace(text)
	if len([]rune(text)) <= maxChars {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:maxChars]))
}
