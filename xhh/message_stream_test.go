package xhh

import (
	"openxhh/config"
	"openxhh/db"
	"testing"
)

func TestMessageStreamTrackDefaults(t *testing.T) {
	oldDays := config.ConfigStruct.Xhh.MessageStreamTrackDays
	oldBatchSize := config.ConfigStruct.Xhh.MessageStreamTrackBatchSize
	config.ConfigStruct.Xhh.MessageStreamTrackDays = 0
	config.ConfigStruct.Xhh.MessageStreamTrackBatchSize = 0
	t.Cleanup(func() {
		config.ConfigStruct.Xhh.MessageStreamTrackDays = oldDays
		config.ConfigStruct.Xhh.MessageStreamTrackBatchSize = oldBatchSize
	})

	if got := messageStreamTrackSince(); got != 0 {
		t.Fatalf("messageStreamTrackSince = %d, want 0 for permanent", got)
	}
	if got := messageStreamTrackBatchSize(); got != defaultMessageStreamTrackBatchSize {
		t.Fatalf("messageStreamTrackBatchSize = %d, want %d", got, defaultMessageStreamTrackBatchSize)
	}
}

func TestFindTrackedBotCommentUsesCommentID(t *testing.T) {
	comments := []CommentInfo{
		{CommentID: 10, UserID: 1, Text: "其他评论"},
		{CommentID: 20, UserID: 2, Text: "机器人回复被小黑盒改写"},
	}
	outbound := db.OutboundMessage{CommentID: 20, Text: "原始机器人回复"}

	got := findTrackedBotComment(comments, outbound)
	if got == nil || got.CommentID != 20 {
		t.Fatalf("findTrackedBotComment = %#v, want comment 20", got)
	}
}

func TestFindTrackedBotCommentUsesNormalizedText(t *testing.T) {
	comments := []CommentInfo{
		{CommentID: 30, UserID: 1, Text: "机器人 回复 [cube_喜欢]"},
	}
	outbound := db.OutboundMessage{Text: "机器人回复"}

	got := findTrackedBotComment(comments, outbound)
	if got == nil || got.CommentID != 30 {
		t.Fatalf("findTrackedBotComment = %#v, want comment 30", got)
	}
}

func TestFindTrackedBotCommentUsesBotIdentityForReply(t *testing.T) {
	oldHeyBoxID := Info.HeyBoxId
	Info.HeyBoxId = "42"
	t.Cleanup(func() { Info.HeyBoxId = oldHeyBoxID })

	comments := []CommentInfo{
		{CommentID: 40, UserID: 7, ReplyID: 10, Text: "普通用户"},
		{CommentID: 41, UserID: 42, ReplyID: 10, Text: "机器人回复被服务端改写"},
	}
	outbound := db.OutboundMessage{RootCommentID: 1, ReplyCommentID: 10, Text: "原始机器人回复"}

	got := findTrackedBotComment(comments, outbound)
	if got == nil || got.CommentID != 41 {
		t.Fatalf("findTrackedBotComment = %#v, want comment 41", got)
	}
}

func TestFindTrackedBotCommentDoesNotGuessWithoutAnchor(t *testing.T) {
	oldHeyBoxID := Info.HeyBoxId
	Info.HeyBoxId = "42"
	t.Cleanup(func() { Info.HeyBoxId = oldHeyBoxID })

	comments := []CommentInfo{{CommentID: 50, UserID: 42, Text: "机器人回复被服务端改写"}}
	outbound := db.OutboundMessage{Text: "原始机器人回复"}

	if got := findTrackedBotComment(comments, outbound); got != nil {
		t.Fatalf("findTrackedBotComment = %#v, want nil", got)
	}
}

func TestFindUnanchoredTopLevelBotComment(t *testing.T) {
	oldHeyBoxID := Info.HeyBoxId
	Info.HeyBoxId = "42"
	t.Cleanup(func() { Info.HeyBoxId = oldHeyBoxID })

	comments := []CommentInfo{
		{CommentID: 50, UserID: 42, Text: "机器人顶级评论"},
		{CommentID: 51, UserID: 7, ReplyID: 50, Text: "未 @ 普通评论"},
	}
	got := findUnanchoredTopLevelBotComment(comments, db.OutboundMessage{Text: "原始机器人回复"})
	if got == nil || got.CommentID != 50 {
		t.Fatalf("findUnanchoredTopLevelBotComment = %#v, want comment 50", got)
	}
}

func TestFindUnanchoredTopLevelBotCommentSkipsChildBotComment(t *testing.T) {
	oldHeyBoxID := Info.HeyBoxId
	Info.HeyBoxId = "42"
	t.Cleanup(func() { Info.HeyBoxId = oldHeyBoxID })

	comments := []CommentInfo{{CommentID: 52, UserID: 42, ReplyID: 10, Text: "机器人楼中楼评论"}}
	if got := findUnanchoredTopLevelBotComment(comments, db.OutboundMessage{Text: "原始机器人回复"}); got != nil {
		t.Fatalf("findUnanchoredTopLevelBotComment = %#v, want nil", got)
	}
}

func TestShouldSaveTrackedInboundForReplyToBot(t *testing.T) {
	oldHeyBoxID := Info.HeyBoxId
	Info.HeyBoxId = "42"
	t.Cleanup(func() { Info.HeyBoxId = oldHeyBoxID })

	comment := CommentInfo{CommentID: 61, UserID: 7, ReplyID: 60, Text: "没 @，但回复机器人"}
	outbound := db.OutboundMessage{Text: "机器人回复"}

	if !shouldSaveTrackedInbound(comment, 10, 60, outbound) {
		t.Fatal("shouldSaveTrackedInbound should save reply to bot")
	}
}

func TestTrackedInboundCommentIDsIncludesNestedRepliesToBotThread(t *testing.T) {
	oldHeyBoxID := Info.HeyBoxId
	Info.HeyBoxId = "42"
	t.Cleanup(func() { Info.HeyBoxId = oldHeyBoxID })

	comments := []CommentInfo{
		{CommentID: 60, UserID: 42, ReplyID: 10, Text: "机器人回复"},
		{CommentID: 61, UserID: 7, ReplyID: 60, Text: "直接回复机器人"},
		{CommentID: 62, UserID: 8, ReplyID: 61, Text: "楼中楼里继续评论"},
		{CommentID: 63, UserID: 9, ReplyID: 10, Text: "同层楼但不是这条对话链"},
	}
	tracked := trackedInboundCommentIDs(comments, 10, 60, db.OutboundMessage{Text: "机器人回复"})

	if !tracked[61] || !tracked[62] {
		t.Fatalf("tracked = %#v, want direct and nested replies", tracked)
	}
	if tracked[63] {
		t.Fatalf("tracked = %#v, should not include unrelated same-floor comment", tracked)
	}
	if got := inboundMessageStreamSource(comments[1], 10, 60); got != "reply_to_bot" {
		t.Fatalf("direct source = %q, want reply_to_bot", got)
	}
	if got := inboundMessageStreamSource(comments[2], 10, 60); got != "nested_reply_to_bot" {
		t.Fatalf("nested source = %q, want nested_reply_to_bot", got)
	}
}

func TestShouldSaveTrackedInboundForBotFloorComment(t *testing.T) {
	oldHeyBoxID := Info.HeyBoxId
	Info.HeyBoxId = "42"
	t.Cleanup(func() { Info.HeyBoxId = oldHeyBoxID })

	comment := CommentInfo{CommentID: 71, UserID: 7, Text: "机器人楼层下的新评论"}
	outbound := db.OutboundMessage{Text: "机器人顶级评论"}

	if !shouldSaveTrackedInbound(comment, 70, 70, outbound) {
		t.Fatal("shouldSaveTrackedInbound should save comment on bot floor")
	}
	if got := inboundMessageStreamSource(comment, 70, 70); got != "comment_on_bot_floor" {
		t.Fatalf("source = %q, want comment_on_bot_floor", got)
	}
}

func TestShouldSaveTrackedInboundSkipsBotAndUnresolvedBotComment(t *testing.T) {
	oldHeyBoxID := Info.HeyBoxId
	Info.HeyBoxId = "42"
	t.Cleanup(func() { Info.HeyBoxId = oldHeyBoxID })

	outbound := db.OutboundMessage{Text: "机器人回复"}
	if shouldSaveTrackedInbound(CommentInfo{CommentID: 81, UserID: 42, ReplyID: 80, Text: "机器人自己"}, 10, 80, outbound) {
		t.Fatal("should skip bot's own comment")
	}
	if shouldSaveTrackedInbound(CommentInfo{CommentID: 82, UserID: 7, ReplyID: 80, Text: "用户回复"}, 10, 0, outbound) {
		t.Fatal("should skip when bot comment is unresolved")
	}
}
