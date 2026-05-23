package xhh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"openxhh/ai"
	"openxhh/config"
	"openxhh/db"
	"openxhh/loger"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

var Info struct {
	Cookie   string `json:"cookie"`
	HeyBoxId string `json:"heyboxId"`
	Time     int    `json:"time"`
}
var CheckTime int
var ReplyTime int
var MaxReplyThreads int
var MaxPendingReplies int
var MaxPendingRepliesPerUser int

const defaultMaxReplyThreads = 3
const defaultMaxPendingReplies = 50
const defaultMaxPendingRepliesPerUser = 5
const maxReplyRetries = 5

var replyRetryCounts sync.Map
const messagePageLimit = 20
const maxMessagePages = 5
const replySchedulerActivePoll = time.Second

const messageTypeAtPost = 16
const messageTypeAtComment = 17

const replyKindOwner = "owner"
const replyKindNormal = "普通用户"

type replyDone struct {
	msgID int
}

func ShouldMentionTarget(text string) bool {
	triggers := []string{"对方", "那个人", "这个人", "楼上", "上面", "艾特他", "艾特她", "提到他", "提到她", "喊他", "喊她", "叫他", "叫她", "回复他", "回复她", "反驳他", "反驳她", "怼他", "怼她", "问问他", "问问她", "告诉他", "告诉她", "安慰他", "安慰她"}
	for _, trigger := range triggers {
		if strings.Contains(text, trigger) {
			return true
		}
	}
	return false
}

func Init() {
	file, err := os.ReadFile("./cookie.json")
	if err != nil {
		loger.Loger.Info("[XHH]未检测到Cookie")
		return
	}
	CheckTime = config.ConfigStruct.Xhh.CheckTime
	ReplyTime = config.ConfigStruct.Xhh.ReplyTime
	MaxReplyThreads = config.ConfigStruct.Xhh.MaxReplyThreads
	MaxPendingReplies = config.ConfigStruct.Xhh.MaxPendingReplies
	MaxPendingRepliesPerUser = config.ConfigStruct.Xhh.MaxPendingRepliesPerUser
	if CheckTime == 0 {
		loger.Loger.Warn("[XHH]您的设置中未设置检查时间，已默认为30s")
		CheckTime = 30
	}
	if ReplyTime == 0 {
		loger.Loger.Warn("[XHH]您的设置中未设置回复间隔，已默认为10s")
		ReplyTime = 10
	}
	if MaxReplyThreads <= 0 {
		loger.Loger.Warn("[XHH]您的设置中未设置最高回复线程，已默认为3")
		MaxReplyThreads = defaultMaxReplyThreads
	}
	if MaxPendingReplies <= 0 {
		loger.Loger.Warn("[XHH]您的设置中未设置最大待回复队列，已默认为50")
		MaxPendingReplies = defaultMaxPendingReplies
	}
	if MaxPendingRepliesPerUser <= 0 {
		loger.Loger.Warn("[XHH]您的设置中未设置单用户最大待回复队列，已默认为5")
		MaxPendingRepliesPerUser = defaultMaxPendingRepliesPerUser
	}
	json.Unmarshal(file, &Info)
}

type Msg struct {
	CommentID     int
	CommentText   string
	MsgID         int
	RootCommentID int
	LinkID        int
	UserID        int
	MessageType   int
	UserName      string
	CreatedAt     int64
	IsPost        bool
}

func (m *Msg) UnmarshalJSON(data []byte) error {
	var aux struct {
		CommentID     int             `json:"comment_a_id"`
		CommentText   string          `json:"comment_a_text"`
		MsgID         int             `json:"message_id"`
		RootCommentID int             `json:"root_comment_id"`
		LinkID        int             `json:"linkid"`
		UserID        json.RawMessage `json:"userid_a"`
		MessageType   int             `json:"message_type"`
		CreatedAt     json.RawMessage `json:"created_at"`
		CreateAt      json.RawMessage `json:"create_at"`
		Time          json.RawMessage `json:"time"`
		Timestamp     json.RawMessage `json:"timestamp"`
		Dateline      json.RawMessage `json:"dateline"`
		MessageTime   json.RawMessage `json:"message_time"`
		User          struct {
			UserID   json.RawMessage `json:"userid"`
			UserName string          `json:"username"`
		} `json:"user_a"`
		Link struct {
			LinkID int    `json:"linkid"`
			Text   string `json:"description"`
		} `json:"link"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	m.CommentID = aux.CommentID
	m.CommentText = aux.CommentText
	m.MsgID = aux.MsgID
	m.RootCommentID = aux.RootCommentID
	m.LinkID = aux.LinkID
	m.UserID = jsonInt(aux.UserID)
	if m.UserID == 0 {
		m.UserID = jsonInt(aux.User.UserID)
	}
	m.MessageType = aux.MessageType
	m.UserName = aux.User.UserName
	m.CreatedAt = firstJSONInt64(aux.CreatedAt, aux.CreateAt, aux.Time, aux.Timestamp, aux.Dateline, aux.MessageTime)
	m.IsPost = aux.MessageType == messageTypeAtPost
	if m.IsPost {
		m.CommentID = -1
		m.RootCommentID = -1
		if aux.Link.LinkID != 0 {
			m.LinkID = aux.Link.LinkID
		}
		if aux.Link.Text != "" {
			m.CommentText = aux.Link.Text
		}
	}
	return nil
}

func jsonInt(raw json.RawMessage) int {
	return int(jsonRawInt64(raw))
}

func firstJSONInt64(values ...json.RawMessage) int64 {
	for _, value := range values {
		if unixTime := normalizeCommentTimeUnixValue(value); unixTime > 0 {
			return unixTime
		}
	}
	return 0
}

func jsonRawInt64(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0
	}
	number, _ = strconv.ParseInt(text, 10, 64)
	return number
}

type Respo struct {
	Msg    string `json:"msg"`
	Result struct {
		Messages []Msg `json:"messages"`
	} `json:"result"`
	Stat    string `json:"stat"`
	Status  string `json:"status"`
	Version string `json:"version"`
}

var DontReply bool
var errInfo struct {
	Count   int
	LastErr int
}

func IsErr() {
	if errInfo.Count < 5 {
		if (int(time.Now().Unix()) - errInfo.LastErr) < 60*10 {
			errInfo.Count = 1
			return
		}
		errInfo.LastErr = int(time.Now().Unix())
		errInfo.Count++
		return
	}
	loger.Loger.Fatal("[XHH]程序十分钟内错误五次，已退出防止频繁")
}

func CheckAt() {
	fmt.Println("[XHH]检查@", time.Now().Format("2006-01-02 15:04:05"))
	if remaining := xhhCaptchaCooldownRemaining(); remaining > 0 {
		xhhCaptchaCoolingDown("user_message")
		time.Sleep(remaining)
		CheckAt()
		return
	}

	for _, messageType := range []int{messageTypeAtPost, messageTypeAtComment} {
		for page := 0; page < maxMessagePages; page++ {
			offset := page * messagePageLimit
			other := fmt.Sprintf("?message_type=%v&offset=%v&limit=%v&no_more=false", messageType, offset, messagePageLimit)
			resp := SendReq("GET", "/bbs/app/user/message", nil, other)
			if resp == nil {
				loger.Loger.Error("[XHH]链接发送失败了")
				IsErr()
				return
			}

			var data Respo
			Dbyte, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				loger.Loger.Error("[XHH]无法读取Body", zap.Error(err))
				IsErr()
				return
			}
			if !isHTTPSuccess(resp.StatusCode) {
				body := string(Dbyte)
				loger.Loger.Warn("[XHH]检查@ HTTP 失败", zap.Int("message_type", messageType), zap.Int("offset", offset), zap.Int("status", resp.StatusCode), zap.String("body", limitXHHResponseBody(body)))
				handleXHHHTTPFailure("user_message", resp.StatusCode, body, zap.Int("message_type", messageType), zap.Int("offset", offset))
				if resp.StatusCode != 403 {
					IsErr()
					return
				}
				if remaining := xhhCaptchaCooldownRemaining(); remaining > 0 {
					time.Sleep(remaining)
				}
				CheckAt()
				return
			}
			err = json.Unmarshal(Dbyte, &data)
			if err != nil {
				loger.Loger.Error("[XHH]无法反序列化", zap.Error(err), zap.String("raw", string(Dbyte)))
				IsErr()
				return
			}

			for _, v := range data.Result.Messages {
				if shouldQueueMessage(v) {
					db.InsertWithUserName(v.MsgID, v.CommentID, v.RootCommentID, v.LinkID, v.UserID, v.UserName, v.CommentText, DontReply)
				}
			}

			if len(data.Result.Messages) < messagePageLimit {
				break
			}
		}
	}

	DontReply = false
	time.Sleep(time.Duration(CheckTime) * time.Second)
	CheckAt()
}

func SyncNotifications() {
	time.Sleep(90 * time.Second)
	for {
		syncNotificationsOnce()
		if time.Now().Hour() == 3 {
			cleanupOldCommentCache()
			time.Sleep(time.Hour) // 避免同小时内重复清理
		}
		time.Sleep(60 * time.Second)
	}
}

func cleanupOldCommentCache() {
	const maxAge = int64(7 * 24 * 60 * 60) // 7 天
	deleted := db.CleanupCommentCache(maxAge)
	if deleted > 0 {
		loger.Loger.Info("[缓存清理]已清理旧评论缓存", zap.Int64("deleted", deleted))
	}
}

func syncNotificationsOnce() {
	if remaining := xhhCaptchaCooldownRemaining(); remaining > 0 {
		return
	}
	saved := 0
	for page := 0; page < maxMessagePages; page++ {
		offset := page * messagePageLimit
		other := fmt.Sprintf("?list_type=0&offset=%v&limit=%v&no_more=false", offset, messagePageLimit)
		resp := SendReq("GET", "/bbs/app/user/message", nil, other)
		if resp == nil {
			return
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return
		}
		if !isHTTPSuccess(resp.StatusCode) {
			handleXHHHTTPFailure("user_message", resp.StatusCode, string(data))
			return
		}
		var msgResp Respo
		if err := json.Unmarshal(data, &msgResp); err != nil {
			return
		}
		stat := msgResp.Stat
		if stat == "" {
			stat = msgResp.Status
		}
		if stat != "ok" {
			if isXHHCaptchaStatus(stat) {
				enterXHHCaptchaCooldown("notification_sync")
			}
			loger.Loger.Warn("[通知同步]API 返回非 ok", zap.String("stat", stat), zap.String("body", string(data[:min(len(data), 300)])))
			return
		}
		for _, v := range msgResp.Result.Messages {
			if db.SaveInboundMessage(db.InboundMessage{
				Source:        "notification",
				MessageID:     int64(v.MsgID),
				LinkID:        int64(v.LinkID),
				RootCommentID: int64(v.RootCommentID),
				CommentID:     int64(v.CommentID),
				UserID:        int64(v.UserID),
				UserName:      CleanXHHRichText(v.UserName),
				Text:          CleanXHHRichText(v.CommentText),
				CreatedAt:     inboundMessageCreatedAt(v),
			}) {
				saved++
			}
		}
		if len(msgResp.Result.Messages) < messagePageLimit {
			break
		}
	}
	if saved > 0 {
		loger.Loger.Info("[通知同步]新增通知", zap.Int("saved", saved))
	}
}

func saveInboundMessageFromApp(v Msg) {
	source := "at_comment"
	if v.IsPost {
		source = "at_post"
	}
	db.SaveInboundMessage(db.InboundMessage{
		Source:        source,
		MessageID:     int64(v.MsgID),
		LinkID:        int64(v.LinkID),
		RootCommentID: int64(v.RootCommentID),
		CommentID:     int64(v.CommentID),
		UserID:        int64(v.UserID),
		UserName:      CleanXHHRichText(v.UserName),
		Text:          CleanXHHRichText(v.CommentText),
		CreatedAt:     inboundMessageCreatedAt(v),
	})
}

func inboundMessageCreatedAt(v Msg) int64 {
	if v.CreatedAt > 0 {
		return v.CreatedAt
	}
	return time.Now().Unix()
}

func shouldQueueMessage(v Msg) bool {
	if !Check(v.UserID) {
		db.InsertWithUserName(v.MsgID, v.CommentID, v.RootCommentID, v.LinkID, v.UserID, v.UserName, v.CommentText, true)
		return false
	}
	if DontReply || IsOwner(v.UserID) {
		return true
	}
	if MaxPendingReplies > 0 && db.PendingReplyCount() >= MaxPendingReplies {
		loger.Loger.Warn("[XHH]待回复队列已满，跳过普通用户@", zap.Int("max", MaxPendingReplies), zap.Int("userid", v.UserID), zap.Int("msg_id", v.MsgID))
		db.InsertWithUserName(v.MsgID, v.CommentID, v.RootCommentID, v.LinkID, v.UserID, v.UserName, v.CommentText, true)
		return false
	}
	if MaxPendingRepliesPerUser > 0 && db.PendingReplyCountByUser(v.UserID) >= MaxPendingRepliesPerUser {
		loger.Loger.Warn("[XHH]单用户待回复队列已满，跳过普通用户@", zap.Int("max", MaxPendingRepliesPerUser), zap.Int("userid", v.UserID), zap.Int("msg_id", v.MsgID))
		db.InsertWithUserName(v.MsgID, v.CommentID, v.RootCommentID, v.LinkID, v.UserID, v.UserName, v.CommentText, true)
		return false
	}
	return true
}

func AutoReply() {
	done := make(chan replyDone, replyThreadLimit()+1)
	inFlight := make(map[int]string)
	for {
		drainCompletedReplies(done, inFlight)
		launched := launchReplyBatch(nextOwnerReplyBatch(inFlight), replyKindOwner, done, inFlight)
		launched = launchReplyBatch(nextNormalReplyBatch(inFlight), replyKindNormal, done, inFlight) || launched
		if !launched && len(inFlight) == 0 {
			fmt.Println("[XHH]无可回复", time.Now().Format("2006-01-02 15:04:05"))
		}
		if len(inFlight) > 0 {
			time.Sleep(replySchedulerActivePoll)
			continue
		}
		time.Sleep(time.Duration(ReplyTime) * time.Second)
	}
}

func drainCompletedReplies(done <-chan replyDone, inFlight map[int]string) {
	for {
		select {
		case item := <-done:
			delete(inFlight, item.msgID)
		default:
			return
		}
	}
}

func launchReplyBatch(replies []db.CommStruct, kind string, done chan<- replyDone, inFlight map[int]string) bool {
	if len(replies) == 0 {
		return false
	}
	loger.Loger.Info("[XHH]正在处理回复批次", zap.String("批次类型", kind), zap.Int("评论数", len(replies)), zap.Int("实际回复线程", len(replies)), zap.Int("普通线程上限", replyThreadLimit()))
	for _, reply := range replies {
		reply := reply
		inFlight[reply.MsgID] = kind
		go func() {
			defer func() { done <- replyDone{msgID: reply.MsgID} }()
			replyComment(reply)
		}()
	}
	return true
}

func nextReplyBatch() []db.CommStruct {
	inFlight := map[int]string{}
	replies := nextOwnerReplyBatch(inFlight)
	return append(replies, nextNormalReplyBatch(inFlight)...)
}

func nextOwnerReplyBatch(inFlight map[int]string) []db.CommStruct {
	return selectReplyBatch(db.GetCommByUserIDs(ownerIDs(), 0), inFlight, 0, replyKindOwner)
}

func nextNormalReplyBatch(inFlight map[int]string) []db.CommStruct {
	slots := replyThreadLimit() - activeReplyCount(inFlight, replyKindNormal)
	if slots <= 0 {
		return nil
	}
	fetchLimit := slots + activeReplyCount(inFlight, replyKindNormal)
	return selectReplyBatch(db.GetCommExcludingUserIDs(ownerIDs(), fetchLimit), inFlight, slots, replyKindNormal)
}

func replyThreadLimit() int {
	if MaxReplyThreads <= 0 {
		return defaultMaxReplyThreads
	}
	return MaxReplyThreads
}

func selectReplyBatch(candidates []db.CommStruct, inFlight map[int]string, limit int, kind string) []db.CommStruct {
	capacity := limit
	if capacity <= 0 {
		capacity = len(candidates)
	}
	replies := make([]db.CommStruct, 0, capacity)
	for _, candidate := range candidates {
		if _, ok := inFlight[candidate.MsgID]; ok {
			continue
		}
		isOwner := IsOwner(candidate.Uid)
		if kind == replyKindOwner && !isOwner {
			continue
		}
		if kind == replyKindNormal && isOwner {
			continue
		}
		replies = append(replies, candidate)
		if limit > 0 && len(replies) >= limit {
			break
		}
	}
	return replies
}

func activeReplyCount(inFlight map[int]string, kind string) int {
	count := 0
	for _, activeKind := range inFlight {
		if activeKind == kind {
			count++
		}
	}
	return count
}

func appendOwnerContext(contents []ai.Content, userID int) []ai.Content {
	if !IsOwner(userID) {
		return contents
	}
	return append(contents, ai.Content{Type: "text", Text: "当前发言用户是机器人 owner。请把这视为身份信息，不要在回复中生硬提及。"})
}

func replyComment(v db.CommStruct) {
	if v.CommentID == 0 {
		fmt.Println("[XHH]无事可做")
		return
	}

	if !Check(v.Uid) {
		db.ReplyedMsg(v.MsgID)
		return
	}

	userText := NormalizeCommentText(v.Text)
	mentionControl := ParseMentionControl(userText)
	loger.Loger.Info("[XHH]正在处理@消息", zap.Int("msg_id", v.MsgID), zap.Int("comment_id", v.CommentID), zap.Int("link_id", v.LinkID), zap.Int("user_id", v.Uid), zap.String("user_name", v.UserName), zap.String("text", mentionQuestionText(mentionControl)), zap.String("cleaned_text", mentionControl.CleanedText), zap.String("raw_text", v.Text), zap.String("mention_target", mentionControl.TargetText), zap.Bool("wake_only", mentionControl.WakeOnly))

	var isok bool
	if mentionControl.WakeOnly {
		isok = replyWithAiComment(v, mentionControl)
	} else if route, routed := routeCommentIntent(v, userText, mentionControl); routed {
		route.MentionTarget = resolveRouteMentionTarget(route.MentionTarget, mentionControl.TargetText, userText)
		if route.MentionTarget != "" {
			mentionControl.TargetText = route.MentionTarget
			mentionControl.HasExplicitTarget = true
		}
		if route.MentionTargetUserID != 0 {
			mentionControl.MentionTargetUserID = route.MentionTargetUserID
		}
		loger.Loger.Info("[XHH]AI路由完成", zap.Int("msg_id", v.MsgID), zap.Int("comment_id", v.CommentID), zap.Int("link_id", v.LinkID), zap.Int("user_id", v.Uid), zap.String("user_name", v.UserName), zap.String("action", route.Action), zap.String("reason", route.Reason), zap.String("image_prompt", route.ImagePrompt), zap.String("mention_target", route.MentionTarget), zap.Int("mention_target_user_id", route.MentionTargetUserID), zap.Bool("needs_post_context", route.NeedsPostContext), zap.Bool("needs_comment_context", route.NeedsCommentContext), zap.Bool("needs_image_input", route.NeedsImageInput), zap.Bool("wants_similar_image", route.WantsSimilarImage))
		switch route.Action {
		case ai.CommentRouteActionImage:
			isok = processRoutedImageComment(v, mentionControl, &route)
		default:
			isok = replyWithAiComment(v, mentionControl)
		}
	} else {
		imageResult := ProcessImageGenerationComment(v.LinkID, v.CommentID, v.RootID, v.Uid, mentionControl.CleanedText, ImageCommentOptions{TriggerUserName: v.UserName, MentionTargetText: mentionControl.TargetText})
		if imageResult.Err != nil {
			loger.Loger.Error("[XHH]图片评论处理失败", zap.Error(imageResult.Err), zap.Int("comment_id", v.CommentID), zap.Int("link_id", v.LinkID))
			if imageResult.Handled {
				isok = true
			}
		} else if imageResult.Handled {
			isok = imageResult.OK
		} else {
			isok = replyWithAiComment(v, mentionControl)
		}
	}

	if isok {
		replyRetryCounts.Delete(v.MsgID)
		db.ReplyedMsg(v.MsgID)
	} else {
		retries, _ := replyRetryCounts.LoadOrStore(v.MsgID, 0)
		count := retries.(int) + 1
		replyRetryCounts.Store(v.MsgID, count)
		IsErr()
		question := mentionQuestionText(mentionControl)
		if question == "" {
			question = v.Text
		}
		failFields := []zap.Field{zap.Int("msg_id", v.MsgID), zap.Int("link_id", v.LinkID), zap.Int("comment_id", v.CommentID), zap.String("user_name", v.UserName), zap.String("text", question), zap.Int("retries", count)}
		if count >= maxReplyRetries {
			loger.Loger.Error("[XHH]无法回复评论，已达最大重试次数，放弃", failFields...)
			replyRetryCounts.Delete(v.MsgID)
			db.ReplyedMsg(v.MsgID)
		} else {
			loger.Loger.Error("[XHH]无法回复评论，将重试", append(failFields, zap.Int("max_retries", maxReplyRetries))...)
		}
	}
}

func routeCommentIntent(v db.CommStruct, userText string, mentionControl MentionControl) (ai.CommentRouteResult, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	routeReq := buildCommentRouteRequest(v, userText, mentionControl)
	route, err := ai.RouteCommentIntent(ctx, routeReq)
	if err != nil {
		loger.Loger.Warn("[XHH]文本模型路由失败，使用规则兜底", zap.Error(err), zap.Int("msg_id", v.MsgID), zap.Int("comment_id", v.CommentID), zap.Int("link_id", v.LinkID), zap.Int("user_id", v.Uid), zap.String("user_name", v.UserName), zap.String("raw_text", v.Text), zap.String("normalized_text", userText), zap.String("cleaned_text", mentionControl.CleanedText), zap.String("mention_target", mentionControl.TargetText), zap.Bool("rule_image_candidate", routeReq.RuleImageCandidate), zap.String("rule_image_prompt", routeReq.RuleImagePrompt), zap.Bool("rule_needs_post_context", routeReq.RuleNeedsPostContext), zap.Bool("rule_needs_comment_context", routeReq.RuleNeedsCommentContext), zap.Bool("rule_needs_image_input", routeReq.RuleNeedsImageInput))
		return ai.CommentRouteResult{}, false
	}
	return route, true
}

func resolveRouteMentionTarget(routeTarget, ruleTarget, userText string) string {
	routeTarget = normalizeMentionControlTarget(routeTarget)
	if routeTarget != "" && !isLeadingWakeMentionTarget(routeTarget, userText) {
		return routeTarget
	}
	ruleTarget = normalizeMentionControlTarget(ruleTarget)
	if ruleTarget != "" {
		return ruleTarget
	}
	return ""
}

func isLeadingWakeMentionTarget(target, text string) bool {
	if target == "" {
		return false
	}
	normalized := strings.TrimSpace(NormalizeCommentText(text))
	for _, m := range mentionTokenPattern.FindAllString(normalized, -1) {
		if normalizeMentionName(strings.TrimPrefix(m, "@")) == target {
			return true
		}
	}
	return false
}

func buildCommentRouteRequest(v db.CommStruct, userText string, mentionControl MentionControl) ai.CommentRouteRequest {
	textForRules := mentionControl.CleanedText
	if textForRules == "" {
		textForRules = userText
	}
	semanticText := mentionQuestionText(mentionControl)
	routeReq := ai.CommentRouteRequest{
		RawComment:        v.Text,
		NormalizedText:    userText,
		CleanedText:       semanticText,
		MentionTarget:     mentionControl.TargetText,
		HasExplicitTarget: mentionControl.HasExplicitTarget,
	}
	if v.RootID != 0 {
		routeReq.CommentContext = GetCommentContextText(v.LinkID, v.RootID, v.CommentID)
	}
	if command, ok := ParseImageCommand(textForRules); ok && !looksLikeImageDiscussion(textForRules) {
		routeReq.RuleImageCandidate = true
		routeReq.RuleImagePrompt = command.Prompt
		if command.UsePostContext || command.UseCommentContext || command.UseImageInput {
			routeReq.RuleImagePrompt = defaultContextImagePrompt(command)
		}
		routeReq.RuleNeedsPostContext = command.UsePostContext
		routeReq.RuleNeedsCommentContext = command.UseCommentContext
		routeReq.RuleNeedsImageInput = command.UseImageInput
		return routeReq
	}
	routeReq.RuleNeedsPostContext = wantsPostContext(textForRules)
	routeReq.RuleNeedsCommentContext = wantsCommentContext(textForRules)
	routeReq.RuleNeedsImageInput = wantsImageInput(textForRules)
	return routeReq
}

func processRoutedImageComment(v db.CommStruct, mentionControl MentionControl, route *ai.CommentRouteResult) bool {
	result := ProcessImageGenerationComment(v.LinkID, v.CommentID, v.RootID, v.Uid, mentionControl.CleanedText, ImageCommentOptions{TriggerUserName: v.UserName, MentionTargetText: mentionControl.TargetText, Route: route})
	if result.Err != nil {
		loger.Loger.Error("[XHH]图片评论处理失败", zap.Error(result.Err), zap.Int("comment_id", v.CommentID), zap.Int("link_id", v.LinkID))
		if result.Handled {
			return true
		}
		return replyWithAiComment(v, mentionControl)
	}
	if result.Handled {
		return result.OK
	}
	return replyWithAiComment(v, mentionControl)
}

func replyWithAiComment(v db.CommStruct, mentionControl MentionControl) bool {
	Info, top, tags, mention := GetLinkInfo(v.LinkID, v.RootID, v.CommentID, v.Uid)
	if len(Info) == 0 {
		loger.Loger.Warn("[XHH]无法整理@消息上下文，使用原评论直接回复", zap.Int("comment_id", v.CommentID), zap.Int("link_id", v.LinkID))
	}
	Info = appendOwnerContext(Info, v.Uid)
	mentionTrigger := ShouldMentionTarget(v.Text)
	mentionTarget := mention != "" && mentionTrigger
	loger.Loger.Info("[XHH]Mention decision", zap.Bool("trigger", mentionTrigger), zap.Bool("hasMention", mention != ""))
	questionText := mentionQuestionText(mentionControl)
	ReplyText := ai.GetAiReply(Info, questionText, top, tags, zap.Int("msg_id", v.MsgID), zap.Int("comment_id", v.CommentID), zap.Int("link_id", v.LinkID), zap.Int("user_id", v.Uid), zap.String("user_name", v.UserName), zap.String("question", questionText), zap.String("raw_question", v.Text))
	if ReplyText == "" {
		loger.Loger.Info("[XHH]Ai返回错误")
		return false
	}
	explicitMention := ""
	if mentionControl.TargetText != "" {
		explicitMention = GetExplicitMentionFromPost(v.LinkID, "艾特"+mentionControl.TargetText, v.Uid)
	}
	if explicitMention == "" {
		explicitMention = GetExplicitMentionFromPost(v.LinkID, mentionControl.CleanedText, v.Uid)
	}
	if mentionControl.MentionTargetUserID != 0 && !isBotUserID(mentionControl.MentionTargetUserID) {
		if username := FindCommentUserName(v.LinkID, v.RootID, v.CommentID, mentionControl.MentionTargetUserID); username != "" {
			ReplyText = buildMention(mentionControl.MentionTargetUserID, username) + " " + ReplyText
		} else if mentionTarget {
			ReplyText = mention + " " + ReplyText
		}
	} else if mentionTarget && isPronounTarget(mentionControl.TargetText) {
		ReplyText = mention + " " + ReplyText
	} else if explicitMention != "" {
		ReplyText = explicitMention + " " + ReplyText
	} else if mentionTarget {
		ReplyText = mention + " " + ReplyText
	}
	return Reply(ReplyText, strconv.Itoa(v.LinkID), strconv.Itoa(v.CommentID), strconv.Itoa(v.RootID), "")
}

func isPronounTarget(target string) bool {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "她", "他", "ta", "楼上":
		return true
	default:
		return false
	}
}

func isBotUserID(userID int) bool {
	return Info.HeyBoxId != "" && strconv.Itoa(userID) == Info.HeyBoxId
}
