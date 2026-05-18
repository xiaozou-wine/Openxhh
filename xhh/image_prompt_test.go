package xhh

import (
	"strings"
	"testing"
	"xhhrobot/ai"
)

func TestSelectImageContextText(t *testing.T) {
	contents := []ai.Content{
		{Type: "text", Text: "以下是帖子内容：\n标题：猫猫新闻"},
		{Type: "text", Text: "正文里说小菲很喜欢机械朋克猫。"},
		{Type: "text", Text: "以下是评论区上下文：小菲说想看画像。"},
		{Type: "image_url"},
	}

	postOnly := selectImageContextText(contents, ImageCommand{UsePostContext: true})
	if !strings.Contains(postOnly, "猫猫新闻") || !strings.Contains(postOnly, "机械朋克猫") {
		t.Fatalf("post context missing expected text: %q", postOnly)
	}
	if strings.Contains(postOnly, "评论区上下文") {
		t.Fatalf("post-only context should not include comment context: %q", postOnly)
	}

	withComments := selectImageContextText(contents, ImageCommand{UsePostContext: true, UseCommentContext: true})
	if !strings.Contains(withComments, "评论区上下文") {
		t.Fatalf("combined context should include comment context: %q", withComments)
	}
}

func TestLimitImageContext(t *testing.T) {
	text := strings.Repeat("猫", imageContextMaxChars+10)
	limited := limitImageContext(text, imageContextMaxChars)
	if got := len([]rune(limited)); got != imageContextMaxChars {
		t.Fatalf("limited length = %d, want %d", got, imageContextMaxChars)
	}
}
