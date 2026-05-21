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
const messageStreamVerboseMissWindow = 10 * time.Minute

func TrackInboundReplies() {
	for {
		processOutboundReplyTrackingOnce()
		time.Sleep(messageStreamTrackInterval)
	}
}

func processOutboundReplyTrackingOnce() {
	since := time.Now().Add(-messageStreamTrackWindow).Unix()
	outbounds := db.RecentOutboundMessages(since, messageStreamTrackLimit)
	if len(outbounds) == 0 {
		return
	}
	var located, unresolved, saved int
	for _, outbound := range outbounds {
		result := trackOutboundReplies(outbound)
		if result.Located {
			located++
		}
		if result.UnresolvedBotComment {
			unresolved++
		}
		saved += result.Saved
	}
	if saved > 0 || unresolved > 0 {
		loger.Loger.Info("[MessageStream]评论我的追踪完成", zap.Int("checked", len(outbounds)), zap.Int("located", located), zap.Int("unresolved_bot_comment", unresolved), zap.Int("saved", saved))
	}
}

type messageStreamTrackResult struct {
	Located              bool
	UnresolvedBotComment bool
	Saved                int
}

type trackedFloorCandidate struct {
	Comments     []CommentInfo
	RootID       int
	BotCommentID int
}

func trackOutboundReplies(outbound db.OutboundMessage) messageStreamTrackResult {
	comments, rootID, botCommentID, ok := trackedFloorComments(outbound)
	if !ok || rootID <= 0 {
		if shouldLogMessageStreamMiss(outbound) {
			loger.Loger.Info("[MessageStream]未定位到机器人评论楼层", messageStreamOutboundFields(outbound, rootID, botCommentID)...)
		}
		return messageStreamTrackResult{}
	}
	result := messageStreamTrackResult{Located: true}
	if botCommentID <= 0 {
		result.UnresolvedBotComment = true
		loger.Loger.Info("[MessageStream]已定位楼层但未定位机器人评论", messageStreamOutboundFields(outbound, rootID, botCommentID)...)
		return result
	}
	if outbound.CommentID <= 0 {
		db.UpdateOutboundMessageComment(outbound.UniqueKey, int64(botCommentID), int64(rootID))
	}
	trackedComments := trackedInboundCommentIDs(comments, rootID, botCommentID, outbound)
	for _, comment := range comments {
		if !trackedComments[comment.CommentID] {
			continue
		}
		source := "reply_to_bot"
		if rootID == botCommentID && comment.ReplyID == 0 {
			source = "comment_on_bot_floor"
		}
		if db.SaveInboundMessage(db.InboundMessage{
			Source:         source,
			LinkID:         outbound.LinkID,
			RootCommentID:  int64(rootID),
			ReplyCommentID: int64(comment.ReplyID),
			CommentID:      int64(comment.CommentID),
			UserID:         int64(comment.UserID),
			UserName:       CleanXHHRichText(comment.User.UserName),
			Text:           CleanXHHRichText(comment.Text),
			CreatedAt:      time.Now().Unix(),
		}) {
			result.Saved++
		}
	}
	if result.Saved > 0 {
		loger.Loger.Info("[MessageStream]已保存评论我的消息", append(messageStreamOutboundFields(outbound, rootID, botCommentID), zap.Int("saved", result.Saved))...)
	}
	return result
}

func trackedFloorComments(outbound db.OutboundMessage) ([]CommentInfo, int, int, bool) {
	if outbound.LinkID <= 0 {
		return nil, 0, 0, false
	}
	maxPage := 1
	var unanchoredBotFloors []trackedFloorCandidate
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
			if botComment := findTrackedBotComment(comments, outbound); botComment != nil {
				return comments, rootID, botComment.CommentID, true
			}
			if botComment := findUnanchoredTopLevelBotComment(comments, outbound); botComment != nil {
				unanchoredBotFloors = append(unanchoredBotFloors, trackedFloorCandidate{Comments: comments, RootID: rootID, BotCommentID: botComment.CommentID})
			}
			botCommentID := int(outbound.CommentID)
			if outbound.RootCommentID > 0 && rootID == int(outbound.RootCommentID) {
				return comments, rootID, botCommentID, true
			}
		}
	}
	if len(unanchoredBotFloors) == 1 {
		candidate := unanchoredBotFloors[0]
		return candidate.Comments, candidate.RootID, candidate.BotCommentID, true
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

func findTrackedBotComment(comments []CommentInfo, outbound db.OutboundMessage) *CommentInfo {
	if outbound.CommentID > 0 {
		if comment := findComment(comments, int(outbound.CommentID)); comment != nil {
			return comment
		}
	}
	if comment := findOutboundCommentByText(comments, outbound.Text); comment != nil {
		return comment
	}
	return findOutboundCommentByBotIdentity(comments, outbound)
}

func findOutboundCommentByBotIdentity(comments []CommentInfo, outbound db.OutboundMessage) *CommentInfo {
	if outbound.RootCommentID <= 0 && outbound.ReplyCommentID <= 0 {
		return nil
	}
	botID, ok := messageStreamBotID()
	if !ok {
		return nil
	}
	var matches []*CommentInfo
	for i := range comments {
		if comments[i].UserID != botID {
			continue
		}
		if outbound.ReplyCommentID > 0 && comments[i].ReplyID != int(outbound.ReplyCommentID) {
			continue
		}
		matches = append(matches, &comments[i])
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return nil
}

func findUnanchoredTopLevelBotComment(comments []CommentInfo, outbound db.OutboundMessage) *CommentInfo {
	if outbound.CommentID > 0 || outbound.RootCommentID > 0 || outbound.ReplyCommentID > 0 || len(comments) == 0 {
		return nil
	}
	botID, ok := messageStreamBotID()
	if !ok {
		return nil
	}
	root := &comments[0]
	if root.CommentID <= 0 || root.UserID != botID || root.ReplyID != 0 {
		return nil
	}
	return root
}

func messageStreamBotID() (int, bool) {
	botID, err := strconv.Atoi(Info.HeyBoxId)
	return botID, err == nil && botID > 0
}

func trackedInboundCommentIDs(comments []CommentInfo, rootID int, botCommentID int, outbound db.OutboundMessage) map[int]bool {
	tracked := map[int]bool{}
	if botCommentID <= 0 {
		return tracked
	}
	if rootID == botCommentID {
		for _, comment := range comments {
			if isTrackableInboundComment(comment, botCommentID, outbound) {
				tracked[comment.CommentID] = true
			}
		}
		return tracked
	}
	related := map[int]bool{botCommentID: true}
	for changed := true; changed; {
		changed = false
		for _, comment := range comments {
			if tracked[comment.CommentID] || !isTrackableInboundComment(comment, botCommentID, outbound) {
				continue
			}
			if !related[comment.ReplyID] {
				continue
			}
			tracked[comment.CommentID] = true
			related[comment.CommentID] = true
			changed = true
		}
	}
	return tracked
}

func shouldSaveTrackedInbound(comment CommentInfo, rootID int, botCommentID int, outbound db.OutboundMessage) bool {
	return trackedInboundCommentIDs([]CommentInfo{comment}, rootID, botCommentID, outbound)[comment.CommentID]
}

func isTrackableInboundComment(comment CommentInfo, botCommentID int, outbound db.OutboundMessage) bool {
	if comment.CommentID <= 0 || comment.CommentID == botCommentID {
		return false
	}
	if Info.HeyBoxId != "" && strconv.Itoa(comment.UserID) == Info.HeyBoxId {
		return false
	}
	return normalizeMessageStreamText(comment.Text) != normalizeMessageStreamText(outbound.Text)
}

func normalizeMessageStreamText(text string) string {
	return strings.Join(strings.Fields(NormalizeCommentText(text)), "")
}

func shouldLogMessageStreamMiss(outbound db.OutboundMessage) bool {
	if outbound.CreatedAt <= 0 {
		return true
	}
	return time.Since(time.Unix(outbound.CreatedAt, 0)) <= messageStreamVerboseMissWindow
}

func messageStreamOutboundFields(outbound db.OutboundMessage, rootID int, botCommentID int) []zap.Field {
	return []zap.Field{
		zap.String("source", outbound.Source),
		zap.Int64("link_id", outbound.LinkID),
		zap.Int64("outbound_root_comment_id", outbound.RootCommentID),
		zap.Int64("outbound_reply_comment_id", outbound.ReplyCommentID),
		zap.Int64("outbound_comment_id", outbound.CommentID),
		zap.Int("located_root_id", rootID),
		zap.Int("located_bot_comment_id", botCommentID),
		zap.String("text", NormalizeCommentText(outbound.Text)),
	}
}

func logMessageStreamTrackingError(message string, err error, fields ...zap.Field) {
	if err == nil {
		loger.Loger.Warn(message, fields...)
		return
	}
	fields = append(fields, zap.Error(err))
	loger.Loger.Warn(message, fields...)
}
