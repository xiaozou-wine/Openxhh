package xhh

import (
	"openxhh/db"
	"openxhh/loger"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

const messageStreamTrackInterval = 120 * time.Second
const messageStreamTrackWindow = 24 * time.Hour
const messageStreamTrackLimit = 120
const messageStreamSubCommentPageBudget = 40

func TrackInboundReplies() {
	for {
		processOutboundReplyTrackingOnce()
		time.Sleep(messageStreamTrackInterval)
	}
}

func processOutboundReplyTrackingOnce() {
	since := time.Now().Add(-messageStreamTrackWindow).Unix()
	for _, outbound := range db.RecentOutboundMessages(since, messageStreamTrackLimit) {
		trackOutboundReplies(outbound)
	}
}

func trackOutboundReplies(outbound db.OutboundMessage) {
	comments, rootID, botCommentID, ok := trackedFloorComments(outbound)
	if !ok || rootID <= 0 {
		return
	}
	if botCommentID > 0 && outbound.CommentID <= 0 {
		db.UpdateOutboundMessageComment(outbound.UniqueKey, int64(botCommentID), int64(rootID))
	}
	for _, comment := range comments {
		if !shouldSaveTrackedInbound(comment, rootID, botCommentID, outbound) {
			continue
		}
		source := "reply_to_bot"
		if botCommentID > 0 && rootID == botCommentID && comment.ReplyID == 0 {
			source = "comment_on_bot_floor"
		}
		db.SaveInboundMessage(db.InboundMessage{
			Source:         source,
			LinkID:         outbound.LinkID,
			RootCommentID:  int64(rootID),
			ReplyCommentID: int64(comment.ReplyID),
			CommentID:      int64(comment.CommentID),
			UserID:         int64(comment.UserID),
			UserName:       NormalizeCommentText(comment.User.UserName),
			Text:           NormalizeCommentText(comment.Text),
			CreatedAt:      time.Now().Unix(),
		})
	}
}

func trackedFloorComments(outbound db.OutboundMessage) ([]CommentInfo, int, int, bool) {
	if outbound.LinkID <= 0 {
		return nil, 0, 0, false
	}
	maxPage := 1
	for page := 1; page <= maxPage && page <= maxMessagePages; page++ {
		resp, ok := fetchLinkInfoPage(int(outbound.LinkID), page)
		if !ok {
			continue
		}
		if resp.Result.TotalPage > maxPage {
			maxPage = resp.Result.TotalPage
		}
		for _, group := range resp.Result.Comments {
			comments, rootID := trackedGroupComments(group)
			if rootID <= 0 {
				continue
			}
			if outbound.RootCommentID > 0 && rootID != int(outbound.RootCommentID) {
				continue
			}
			botCommentID := int(outbound.CommentID)
			if botCommentID > 0 && findComment(comments, botCommentID) != nil {
				return comments, rootID, botCommentID, true
			}
			if match := findOutboundCommentByText(comments, outbound.Text); match != nil {
				return comments, rootID, match.CommentID, true
			}
			if outbound.RootCommentID > 0 && rootID == int(outbound.RootCommentID) {
				return comments, rootID, botCommentID, true
			}
		}
	}
	return nil, 0, 0, false
}

func trackedGroupComments(group commentGroup) ([]CommentInfo, int) {
	if len(group.Comment) == 0 || group.Comment[0].CommentID <= 0 {
		return nil, 0
	}
	comments := make([]CommentInfo, len(group.Comment))
	copy(comments, group.Comment)
	budget := messageStreamSubCommentPageBudget
	rootID := comments[0].CommentID
	return fetchAllSubComments(rootID, comments, &budget), rootID
}

func findOutboundCommentByText(comments []CommentInfo, text string) *CommentInfo {
	target := normalizeMessageStreamText(text)
	if target == "" {
		return nil
	}
	for i := range comments {
		if normalizeMessageStreamText(comments[i].Text) == target {
			return &comments[i]
		}
	}
	return nil
}

func shouldSaveTrackedInbound(comment CommentInfo, rootID int, botCommentID int, outbound db.OutboundMessage) bool {
	if comment.CommentID <= 0 || comment.CommentID == botCommentID {
		return false
	}
	if Info.HeyBoxId != "" && strconv.Itoa(comment.UserID) == Info.HeyBoxId {
		return false
	}
	if normalizeMessageStreamText(comment.Text) == normalizeMessageStreamText(outbound.Text) {
		return false
	}
	if botCommentID <= 0 {
		return false
	}
	if comment.ReplyID == botCommentID {
		return true
	}
	if rootID == botCommentID && comment.CommentID != botCommentID {
		return true
	}
	return false
}

func normalizeMessageStreamText(text string) string {
	return strings.Join(strings.Fields(NormalizeCommentText(text)), "")
}

func logMessageStreamTrackingError(message string, err error, fields ...zap.Field) {
	if err == nil {
		loger.Loger.Warn(message, fields...)
		return
	}
	fields = append(fields, zap.Error(err))
	loger.Loger.Warn(message, fields...)
}
