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
	return GetAiReplyWithPrompt(config.ConfigStruct.Ai.Prompt, Contents, UserSay, Topics, Tags, logFields...)
}

func GetAiReplyWithPrompt(prompt string, Contents []Content, UserSay string, Topics []Topics, Tags []Tags, logFields ...zap.Field) string {
	askFields := append([]zap.Field{zap.Any("Content", Contents)}, logFields...)
	loger.Loger.Info("[Ai]正在询问Ai", askFields...)
	var SMsg Messages[string]
	var UMsg Messages[[]Content]
	var Msgs []any
	SMsg.Role = "system"
	prompt = applyPromptVariables(prompt, Topics, Tags)
	fmt.Println(prompt)
	SMsg.Content = prompt
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
	if isRejectionReply(text) {
		loger.Loger.Warn("[Ai]Ai拒绝回答（安全审核）", append(logFields, zap.String("text", text))...)
		return ""
	}
	return text
}

func isRejectionReply(text string) bool {
	lower := strings.ToLower(text)
	for _, p := range rejectionPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

var rejectionPatterns = []string{
	"request was rejected",
	"high risk",
	"content_policy",
	"content policy violation",
	"safety system",
	"blocked by openai",
	"refused to respond",
	"unable to process this request",
	"违反了内容政策",
	"违规内容",
}

func applyPromptVariables(prompt string, Topics []Topics, Tags []Tags) string {
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
	return prompt
}
