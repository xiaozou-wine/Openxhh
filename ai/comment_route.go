package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"openxhh/config"
	"strings"
	"time"
)

const (
	CommentRouteActionReply      = "reply"
	CommentRouteActionImage      = "image"
	CommentRouteActionIgnore     = "ignore"
	CommentRouteActionRegenerate = "regenerate"
)

type CommentRouteRequest struct {
	RawComment              string
	NormalizedText          string
	CleanedText             string
	MentionTarget           string
	HasExplicitTarget       bool
	RuleImageCandidate      bool
	RuleImagePrompt         string
	RuleNeedsPostContext    bool
	RuleNeedsCommentContext bool
	RuleNeedsImageInput     bool
	CommentContext          string
}

type CommentRouteResult struct {
	Action              string `json:"action"`
	ImagePrompt         string `json:"image_prompt"`
	MentionTarget       string `json:"mention_target"`
	MentionTargetUserID int    `json:"mention_target_user_id"`
	NeedsPostContext    bool   `json:"needs_post_context"`
	NeedsCommentContext bool   `json:"needs_comment_context"`
	NeedsImageInput     bool   `json:"needs_image_input"`
	WantsSimilarImage   bool   `json:"wants_similar_image"`
	Reason              string `json:"reason"`
}

func RouteCommentIntent(ctx context.Context, req CommentRouteRequest) (CommentRouteResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	model := config.ConfigStruct.Ai.Model
	if strings.TrimSpace(model) == "" {
		return CommentRouteResult{}, errors.New("ai model is not configured")
	}

	content, err := sendChatCompletion(ctx, model, []chatCompletionMessage{
		{Role: "system", Content: commentRouteSystemPrompt()},
		{Role: "user", Content: buildCommentRoutePrompt(req)},
	})
	if err != nil {
		return CommentRouteResult{}, fmt.Errorf("comment route request failed: %w", err)
	}
	result, err := ParseCommentRouteContent(content, req.MentionTarget)
	if err != nil {
		return CommentRouteResult{}, fmt.Errorf("parse comment route response failed: %w content=%s", err, limitIntentContext(content))
	}
	return applyCommentRouteRuleHints(result, req), nil
}

func ParseCommentRouteContent(content string, fallbackMentionTarget string) (CommentRouteResult, error) {
	var result CommentRouteResult
	if err := json.Unmarshal([]byte(extractJSONText(content)), &result); err != nil {
		return CommentRouteResult{}, err
	}
	result.Action = normalizeCommentRouteAction(result.Action)
	result.ImagePrompt = strings.TrimSpace(result.ImagePrompt)
	result.MentionTarget = strings.TrimSpace(result.MentionTarget)
	result.Reason = strings.TrimSpace(result.Reason)
	if result.MentionTarget == "" {
		result.MentionTarget = strings.TrimSpace(fallbackMentionTarget)
	}
	return result, nil
}

func applyCommentRouteRuleHints(result CommentRouteResult, req CommentRouteRequest) CommentRouteResult {
	if result.Action != CommentRouteActionImage {
		return result
	}
	if result.ImagePrompt == "" {
		result.ImagePrompt = strings.TrimSpace(req.RuleImagePrompt)
	}
	if result.ImagePrompt == "" {
		result.ImagePrompt = defaultImagePromptFromRouteHints(req)
	}
	result.NeedsPostContext = result.NeedsPostContext || req.RuleNeedsPostContext
	result.NeedsCommentContext = result.NeedsCommentContext || req.RuleNeedsCommentContext
	result.NeedsImageInput = result.NeedsImageInput || req.RuleNeedsImageInput
	return result
}

func defaultImagePromptFromRouteHints(req CommentRouteRequest) string {
	if req.RuleNeedsImageInput && req.RuleNeedsCommentContext {
		return "根据参考图片和当前评论楼层内容生成图片"
	}
	if req.RuleNeedsImageInput && req.RuleNeedsPostContext {
		return "根据参考图片和帖子内容生成图片"
	}
	if req.RuleNeedsImageInput {
		return "根据参考图片生成类似图片"
	}
	if req.RuleNeedsCommentContext {
		return "根据当前评论楼层内容生成图片"
	}
	if req.RuleNeedsPostContext {
		return "根据帖子内容生成图片"
	}
	return ""
}

func normalizeCommentRouteAction(action string) string {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "image", "generate_image", "image_generation", "draw", "生图", "画图", "生成图片", "看图生图", "图生图":
		return CommentRouteActionImage
	case "ignore", "skip", "none", "no_reply", "不处理", "忽略":
		return CommentRouteActionReply
	case "regenerate", "regen", "retry", "重新生成", "重生成":
		return CommentRouteActionRegenerate
	case "reply", "chat", "answer", "普通回复", "回复":
		return CommentRouteActionReply
	default:
		return CommentRouteActionReply
	}
}

func commentRouteSystemPrompt() string {
	return `你是 Openxhh 的 @ 评论路由器，只输出 JSON，不要 Markdown，不要解释。
你的任务是阅读整条评论，先判断机器人应该走哪个动作，再给出必要字段。

可选 action：
- reply：普通 AI 文字回复。只要这条评论是 @ 唤醒机器人，默认都应该回复。
- image：生成图片。包括请求生成图片、画图、出图、做海报/头像/表情包/插画，以及根据正文/帖子/评论区/当前楼层/参考图片生成图片。
- regenerate：用户明确要求重新生成上一条机器人输出。

规则：
1. 所有判断必须基于整条评论语义；机器人 @ 只是唤醒标记，可能在开头、中间或结尾。
2. 只要被 @ 唤醒，默认 action 选 reply；对白名单用户的限制交给白名单开关做，路由层不要替代这个开关。
3. 先让模型判断：这条评论是不是在要求机器人回复、转述、评价、带话，或者是在明确要艾特某个人。
4. 如果评论里有明确的艾特意图，就把目标人写进 mention_target；如果目标不唯一，优先保守留空，不要乱 @。
5. 下面这些都只是示例，不是硬关键词匹配：
   - @张三 你看看
   - 喊李四来评评理
   - 让王五出来说话
   - 叫赵六过来看看
   - 咬小明这句话
   - 抓一下这个人
6. AI 判断 mention_target 时，优先结合当前评论、被回复评论、楼层上下文和对话指向，而不是只看一个词。
7. 如果用户要求“根据正文/文章/帖子/原帖/评论区/这层楼/这张图片”，对应 needs_post_context、needs_comment_context、needs_image_input 必须为 true。
8. 看图生图、图生图、参考这张图、类似这张图、把这张图改成，都选 image 且 needs_image_input=true。
9. “艾特谁来看、给谁看、让谁看、回复谁、喊谁来看”属于 mention_target，不要写进 image_prompt。
10. 机器人自己的 @ 只是唤醒标记，绝不能作为 mention_target；mention_target 只能来自用户明确要求艾特、给、让、回复、喊的人名。
11. action=image 时，image_prompt 必须是适合图片生成模型的画面描述，主体优先来自用户指定的上下文来源。
12. 如果用户要求根据帖子/评论/图片生成，当前路由阶段看不到完整上下文，不要凭空编造主体；image_prompt 应保留“根据帖子内容/当前评论楼层/参考图片生成...”这类上下文指向，后续 prompt refine 会填入细节。
13. 用户附带的祝福、吐槽、夸奖、安慰、整活短句只作为画面情绪、立场或用途，不要覆盖上下文主体。
14. 输出 JSON 格式：{"action":"reply","image_prompt":"","mention_target":"","mention_target_user_id":0,"needs_post_context":false,"needs_comment_context":false,"needs_image_input":false,"wants_similar_image":false,"reason":"..."}
15. 如果评论上下文中提供了 [user_id:xxx] 标记，当你能确定 mention_target 指向某个具体用户时，必须输出 mention_target_user_id 为该用户的 user_id 数值。例如上下文里有 [user_id:87108878] 永雏小菲official 发了言，而用户说"反驳她"，则 mention_target_user_id 应为 87108878。
16. 如果无法从上下文确定具体用户（比如代词指向不明确），mention_target_user_id 留 0。`
}

func buildCommentRoutePrompt(req CommentRouteRequest) string {
	commentContext := req.CommentContext
	if commentContext == "" {
		commentContext = "无"
	}
	return fmt.Sprintf(`原始评论：%s
归一化文本：%s
清洗后文本：%s
解析层 mention 候选（仅候选，不是最终结果）：%s
是否显式目标 mention：%v
规则层是否命中生图候选：%v
规则层 prompt：%s
规则层上下文标记：needs_post_context=%v, needs_comment_context=%v, needs_image_input=%v

评论楼层上下文（每行格式 [user_id:xxx] 用户名：内容）：
%s

请输出路由 JSON。无法确定时 action 选 reply。若 action=image，请给出最终 image_prompt 和所需上下文标记。
`,
		limitIntentContext(req.RawComment),
		limitIntentContext(req.NormalizedText),
		limitIntentContext(req.CleanedText),
		req.MentionTarget,
		req.HasExplicitTarget,
		req.RuleImageCandidate,
		limitIntentContext(req.RuleImagePrompt),
		req.RuleNeedsPostContext,
		req.RuleNeedsCommentContext,
		req.RuleNeedsImageInput,
		commentContext,
	)
}
