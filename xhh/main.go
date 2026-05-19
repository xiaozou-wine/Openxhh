package xhh

import (
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
const messagePageLimit = 20
const maxMessagePages = 5

const messageTypeAtPost = 16
const messageTypeAtComment = 17

func ShouldMentionTarget(text string) bool {
	triggers := []string{"他", "她", "对方", "那个人", "这个人", "楼上", "上面", "反驳", "告诉", "问问", "回复他", "回复她", "怼"}
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
	if len(raw) == 0 {
		return 0
	}
	var number int
	if err := json.Unmarshal(raw, &number); err == nil {
		return number
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0
	}
	number, _ = strconv.Atoi(text)
	return number
}

type Respo struct {
	Msg    string `json:"msg"`
	Result struct {
		Messages []Msg `json:"messages"`
	} `json:"result"`
	Stat    string `json:"stat"`
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
	for {
		Arr := db.GetComm(MaxReplyThreads)
		if len(Arr) == 0 {
			fmt.Println("[XHH]无可回复", time.Now().Format("2006-01-02 15:04:05"))
			time.Sleep(time.Duration(ReplyTime) * time.Second)
			continue
		}

		workerCount := MaxReplyThreads
		if workerCount <= 0 {
			workerCount = defaultMaxReplyThreads
		}
		if workerCount > len(Arr) {
			workerCount = len(Arr)
		}

		jobs := make(chan db.CommStruct)
		var wg sync.WaitGroup
		loger.Loger.Info("[XHH]正在回复评论", zap.Int("评论数", len(Arr)), zap.Int("最高回复线程", workerCount))
		for i := 0; i < workerCount; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for v := range jobs {
					replyComment(v)
				}
			}()
		}
		for _, v := range Arr {
			jobs <- v
		}
		close(jobs)
		wg.Wait()
		time.Sleep(time.Duration(ReplyTime) * time.Second)
	}
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
	loger.Loger.Info("[XHH]正在处理@消息", zap.Int("msg_id", v.MsgID), zap.Int("comment_id", v.CommentID), zap.Int("link_id", v.LinkID), zap.Int("user_id", v.Uid), zap.String("user_name", v.UserName), zap.String("text", userText), zap.String("raw_text", v.Text))

	var isok bool
	handledImage, imageOK := HandleImageGenerationComment(v.LinkID, v.CommentID, v.RootID, v.Uid, v.UserName, userText)
	if handledImage {
		isok = imageOK
	} else {
		Info, top, tags, mention := GetLinkInfo(v.LinkID, v.RootID, v.CommentID, v.Uid)
		if len(Info) <= 1 {
			loger.Loger.Info("[XHH]无法整理@消息，已标记完成避免阻塞", zap.Int("comment_id", v.CommentID), zap.Int("link_id", v.LinkID))
			db.ReplyedMsg(v.MsgID)
			return
		}
		Info = appendOwnerContext(Info, v.Uid)
		mentionTrigger := ShouldMentionTarget(userText)
		mentionTarget := mention != "" && mentionTrigger
		loger.Loger.Info("[XHH]Mention decision", zap.Bool("trigger", mentionTrigger), zap.Bool("hasMention", mention != ""))
		ReplyText := ai.GetAiReply(Info, userText, top, tags, zap.Int("msg_id", v.MsgID), zap.Int("comment_id", v.CommentID), zap.Int("link_id", v.LinkID), zap.Int("user_id", v.Uid), zap.String("user_name", v.UserName), zap.String("question", userText), zap.String("raw_question", v.Text))
		if ReplyText == "" {
			loger.Loger.Info("[XHH]Ai返回错误")
			IsErr()
			return
		}
		explicitMention := GetExplicitMentionFromPost(v.LinkID, userText, v.Uid)
		if explicitMention != "" {
			ReplyText = explicitMention + " " + ReplyText
		} else if mentionTarget {
			ReplyText = mention + " " + ReplyText
		}
		isok = Reply(ReplyText, strconv.Itoa(v.LinkID), strconv.Itoa(v.CommentID), strconv.Itoa(v.RootID), "")
	}

	if isok {
		db.ReplyedMsg(v.MsgID)
	} else {
		IsErr()
		loger.Loger.Error("[XHH]无法回复评论")
	}
}
