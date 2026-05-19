package xhh

import (
	"html"
	"regexp"
	"strings"
)

type ImageCommand struct {
	Prompt            string
	RawPrompt         string
	Trigger           string
	UsePostContext    bool
	UseCommentContext bool
	UseImageInput     bool
	MentionTargetText string
}

var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)
var leadingMentionPattern = regexp.MustCompile(`^\s*(?:@\S+\s*)+`)
var leadingImageObjectPattern = regexp.MustCompile(`^\s*(?:图片|图像|图)\s*[，,。:：、\s]+`)
var imageWeakTriggerPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:帮我|请|可以|能不能)?\s*(生成|画|来|做|出)\s*(一张|一幅|一个|一只|一位|张|幅|个|只|位)\s*(.+)`),
	regexp.MustCompile(`(?:帮我|请|可以|能不能)?\s*(画|生成)\s*(.+)`),
}
var mentionControlPattern = regexp.MustCompile(`(?:并|，|,|。|、|\s)*(?:顺便|帮我|请|可以|能不能)?(?:艾特|提到|喊|叫)\s*(?:她|他|ta|TA|@?[^\s，,。.!！?？:：、@]{1,24})?(?:看看|查看|看下|来看|评价|一下)?`)
var atControlPattern = regexp.MustCompile(`(?:并|，|,|。|、|\s)*(?:顺便|帮我|请|可以|能不能)\s*@[^\s，,。.!！?？:：、@]{1,24}(?:看看|查看|看下|来看|评价|一下)?`)
var contextControlPattern = regexp.MustCompile(`(?:根据|基于|按照|按)(?:这个|这篇|这条|本条|当前|该|本)?(?:正文|文章|帖子|原帖|评论区|评论|楼里|楼上)(?:内容)?`)
var imageInputControlPattern = regexp.MustCompile(`(?:参考|按照|按|基于)?(?:这张图|这个图|图片|原图|评论里的图|楼里的图)|图生图|改图`)
var pronounMentionPattern = regexp.MustCompile(`(?:艾特|提到|喊|叫)\s*(她|他|ta|TA)`)
var portraitSubjectPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:一张|一幅|张|幅)?\s*([^\s，,。.!！?？:：、@]{1,24})的(?:画像|照片|头像|图|插画|肖像)`),
	regexp.MustCompile(`(?:画|生成|做|来|出)\s*(?:一张|一幅|张|幅)?\s*([^\s，,。.!！?？:：、@]{1,24})(?:画像|照片|头像|图|插画|肖像)`),
}

func ExtractImagePrompt(text string) (string, bool) {
	command, ok := ParseImageCommand(text)
	return command.Prompt, ok
}

func ParseImageCommand(text string) (ImageCommand, bool) {
	cleaned := normalizeImageCommandText(text)
	command := ImageCommand{
		UsePostContext:    wantsPostContext(cleaned),
		UseCommentContext: wantsCommentContext(cleaned),
		UseImageInput:     wantsImageInput(cleaned),
		MentionTargetText: extractImageMentionTarget(cleaned),
	}

	prompt, trigger, ok := extractImagePromptWithTrigger(cleaned)
	if !ok {
		return ImageCommand{}, false
	}
	command.RawPrompt = strings.TrimSpace(prompt)
	command.Trigger = trigger
	command.Prompt = cleanupImagePrompt(prompt)
	if shouldUseContextForPortrait(command.Prompt) {
		command.UsePostContext = true
		command.UseCommentContext = true
	}
	if command.Prompt == "" {
		return ImageCommand{}, false
	}
	return command, true
}

func NormalizeCommentText(text string) string {
	cleaned := html.UnescapeString(htmlTagPattern.ReplaceAllString(text, " "))
	cleaned = xhhEmojiPattern.ReplaceAllString(cleaned, "")
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return strings.TrimSpace(cleaned)
}

func normalizeImageCommandText(text string) string {
	cleaned := NormalizeCommentText(text)
	cleaned = leadingMentionPattern.ReplaceAllString(cleaned, "")
	return strings.TrimSpace(cleaned)
}

func extractImagePromptWithTrigger(text string) (string, string, bool) {
	keywords := []string{"生成图片", "生图", "画图"}
	for _, keyword := range keywords {
		idx := strings.Index(text, keyword)
		if idx < 0 {
			continue
		}
		return text[idx+len(keyword):], keyword, true
	}

	for _, pattern := range imageWeakTriggerPatterns {
		match := pattern.FindStringSubmatch(text)
		if len(match) < 3 {
			continue
		}
		prompt := match[len(match)-1]
		trigger := match[1]
		if looksLikeImageIntent(text, prompt) {
			return prompt, trigger, true
		}
	}
	return "", "", false
}

func looksLikeImageIntent(text string, prompt string) bool {
	textTaskWords := []string{"回复", "文案", "文章", "摘要", "总结", "代码", "脚本", "表格", "清单", "标题", "评论"}
	for _, word := range textTaskWords {
		if strings.Contains(prompt, word) {
			return false
		}
	}

	imageWords := []string{"图", "图片", "画像", "照片", "海报", "插画", "壁纸", "头像", "表情包", "梗图", "封面", "画", "猫", "狗", "猫娘", "角色", "人物", "女孩", "男孩", "少女", "少年", "机器人", "怪物", "动物"}
	for _, word := range imageWords {
		if strings.Contains(text, word) || strings.Contains(prompt, word) {
			return true
		}
	}
	measureWords := []string{"一张", "一幅", "一只", "一位", "张", "幅", "只", "位"}
	for _, word := range measureWords {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

func cleanupImagePrompt(prompt string) string {
	cleaned := prompt
	cleaned = xhhEmojiPattern.ReplaceAllString(cleaned, "")
	cleaned = contextControlPattern.ReplaceAllString(cleaned, "")
	cleaned = imageInputControlPattern.ReplaceAllString(cleaned, "")
	cleaned = mentionControlPattern.ReplaceAllString(cleaned, "")
	cleaned = atControlPattern.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimLeft(cleaned, "：:，,。.!！、 ")
	cleaned = leadingImageObjectPattern.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimLeft(cleaned, "：:，,。.!！、 ")
	cleaned = strings.TrimSpace(cleaned)
	return cleaned
}

func wantsPostContext(text string) bool {
	triggers := []string{"根据正文", "根据文章", "根据文章内容", "根据帖子", "根据原帖", "基于正文", "基于文章", "基于帖子", "按这个帖子", "按照这个帖子", "按这篇文章", "按照这篇文章"}
	for _, trigger := range triggers {
		if strings.Contains(text, trigger) {
			return true
		}
	}
	return false
}

func wantsCommentContext(text string) bool {
	triggers := []string{"根据评论区", "根据评论", "根据这条评论", "根据本条评论", "根据当前评论", "根据楼里", "根据楼上", "基于评论区", "基于评论", "按评论区", "按照评论区"}
	for _, trigger := range triggers {
		if strings.Contains(text, trigger) {
			return true
		}
	}
	return false
}

func wantsImageInput(text string) bool {
	triggers := []string{"参考这张图", "参考图片", "按图", "按照图", "图生图", "改图", "把这张图改成", "根据这张图"}
	for _, trigger := range triggers {
		if strings.Contains(text, trigger) {
			return true
		}
	}
	return false
}

func shouldUseContextForPortrait(prompt string) bool {
	for _, pattern := range portraitSubjectPatterns {
		match := pattern.FindStringSubmatch(prompt)
		if len(match) >= 2 && normalizeControlMentionTarget(match[1]) != "" {
			return true
		}
	}
	return false
}

func extractImageMentionTarget(text string) string {
	for _, pattern := range explicitMentionTargetPatterns {
		match := pattern.FindStringSubmatch(text)
		if len(match) >= 2 {
			target := normalizeControlMentionTarget(match[1])
			if target != "" {
				return target
			}
		}
	}
	if !pronounMentionPattern.MatchString(text) {
		return ""
	}
	for _, pattern := range portraitSubjectPatterns {
		match := pattern.FindStringSubmatch(text)
		if len(match) >= 2 {
			target := normalizeControlMentionTarget(match[1])
			if target != "" {
				return target
			}
		}
	}
	return ""
}

func normalizeControlMentionTarget(target string) string {
	target = strings.TrimSpace(target)
	target = strings.TrimPrefix(target, "@")
	target = strings.Trim(target, "：:，,。.!！?？、")
	for _, prefix := range []string{"一张", "一幅", "一个", "张", "幅", "个"} {
		target = strings.TrimPrefix(target, prefix)
	}
	for _, suffix := range []string{"看看", "查看", "看下", "来看", "评价", "一下"} {
		target = strings.TrimSuffix(target, suffix)
	}
	target = strings.Trim(target, "：:，,。.!！?？、")
	if target == "" || target == "她" || target == "他" || strings.EqualFold(target, "ta") || strings.Contains(target, "机器人") {
		return ""
	}
	return target
}
