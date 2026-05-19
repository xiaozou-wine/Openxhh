package ai

import (
	"fmt"
	"openxhh/config"
	"openxhh/loger"
	"strings"

	"go.uber.org/zap"
)

type Topics struct {
	Name string `json:"name"`
}
type Tags struct {
	Name string `json:"name"`
}

func GetAiReply(Contents []Content, UserSay string, Topics []Topics, Tags []Tags, logFields ...zap.Field) string {
	askFields := append([]zap.Field{zap.Any("Content", Contents)}, logFields...)
	loger.Loger.Info("[Ai]正在询问Ai", askFields...)
	var SMsg Messages[string]
	var UMsg Messages[[]Content]
	var Msgs []any
	SMsg.Role = "system"
	cfg := config.ConfigStruct.Ai
	prompt := cfg.Prompt
	var topStr strings.Builder
	for _, v := range Topics {
		topStr.WriteString(v.Name)
	}
	prompt = strings.ReplaceAll(prompt, "?!top!?", topStr.String())
	var tagStr strings.Builder
	for _, v := range Tags {
		tagStr.WriteString(v.Name)
	}
	prompt = strings.ReplaceAll(prompt, "?!tag!?", tagStr.String())
	fmt.Println(prompt)
	SMsg.Content = prompt
	//用户
	UMsg.Role = "user"
	var UserContent Content
	UserContent.Text = "以上是帖子内容。\n用户整条评论：" + UserSay + "\n请结合整条评论理解用户意图；评论中的机器人 @ 只是唤醒标记，可能出现在开头、中间或结尾，不要把它当作问题起点。"
	UserContent.Type = "text"
	Contents = append(Contents, UserContent)
	UMsg.Content = Contents
	Msgs = append(Msgs, SMsg)
	Msgs = append(Msgs, UMsg)
	aiModel := config.ConfigStruct.Ai.Model
	resp := SendReq(aiModel, Msgs)
	if len(resp.Choices) == 0 {
		loger.Loger.Error("[Ai]Ai返回错误", zap.Any("Resp", resp))
		return ""
	}
	text := resp.Choices[0].Msg.Content
	appendTokenRecord(aiModel, resp.Usage.TotalToken)
	replyFields := append([]zap.Field{zap.String("text", text), zap.Int("本次消耗token", resp.Usage.TotalToken)}, logFields...)
	loger.Loger.Info("[Ai]Ai说：", replyFields...)
	return text
}
