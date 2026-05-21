package xhh

import (
	"context"
	"errors"
	"fmt"
	"openxhh/ai"
	"openxhh/config"
	"openxhh/loger"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

type ImageCommentOptions struct {
	DryRun            bool
	MockImage         bool
	TriggerUserName   string
	MentionTargetText string
	Route             *ai.CommentRouteResult
}

type ImageCommentResult struct {
	Handled bool
	OK      bool
	Err     error
}

const (
	defaultImageReplyText = "图片来了喵"
	maxImageReplyRunes    = 40
)

func HandleImageGenerationComment(linkID, commentID, rootID, userID int, userName, text string) (bool, bool) {
	result := ProcessImageGenerationComment(linkID, commentID, rootID, userID, text, ImageCommentOptions{TriggerUserName: userName})
	if result.Err != nil {
		loger.Loger.Error("[XHH]图片评论处理失败", zap.Error(result.Err), zap.Int("comment_id", commentID), zap.Int("link_id", linkID))
		if result.Handled {
			return true, true
		}
	}
	return result.Handled, result.OK
}

func imageCommandFromRoute(route *ai.CommentRouteResult) (ImageCommand, bool) {
	if route == nil || route.Action != ai.CommentRouteActionImage || strings.TrimSpace(route.ImagePrompt) == "" {
		return ImageCommand{}, false
	}
	prompt := strings.TrimSpace(route.ImagePrompt)
	return ImageCommand{
		Prompt:            prompt,
		RawPrompt:         prompt,
		Trigger:           "ai_route",
		UsePostContext:    route.NeedsPostContext,
		UseCommentContext: route.NeedsCommentContext,
		UseImageInput:     route.NeedsImageInput || route.WantsSimilarImage,
		MentionTargetText: route.MentionTarget,
	}, true
}

func applyRouteToImageCommand(command *ImageCommand, route *ai.CommentRouteResult) {
	if command == nil || route == nil || route.Action != ai.CommentRouteActionImage {
		return
	}
	if prompt := strings.TrimSpace(route.ImagePrompt); prompt != "" {
		command.Prompt = prompt
		command.RawPrompt = prompt
	}
	if route.MentionTarget != "" {
		command.MentionTargetText = route.MentionTarget
	}
	command.UsePostContext = command.UsePostContext || route.NeedsPostContext
	command.UseCommentContext = command.UseCommentContext || route.NeedsCommentContext
	command.UseImageInput = command.UseImageInput || route.NeedsImageInput || route.WantsSimilarImage
}

func ProcessImageGenerationComment(linkID, commentID, rootID, userID int, text string, options ImageCommentOptions) ImageCommentResult {
	command, ok := ParseImageCommand(text)
	if !ok {
		command, ok = imageCommandFromRoute(options.Route)
		if !ok {
			return ImageCommentResult{}
		}
	} else {
		applyRouteToImageCommand(&command, options.Route)
	}
	if options.MentionTargetText != "" {
		command.MentionTargetText = options.MentionTargetText
	}
	prompt := command.Prompt
	generationPrompt := prompt
	if !Check(userID) {
		if options.DryRun {
			fmt.Printf("dry-run: unauthorized user ignored, comment_id=%d userid=%d\n", commentID, userID)
		}
		return ImageCommentResult{Handled: true, OK: true}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if options.Route == nil || options.Route.Action != ai.CommentRouteActionImage {
		intent, err := ai.UnderstandImageIntent(ctx, ai.ImageIntentRequest{
			RawComment:        text,
			NormalizedText:    text,
			CleanedText:       text,
			MentionTarget:     command.MentionTargetText,
			UsePostContext:    command.UsePostContext,
			UseCommentContext: command.UseCommentContext,
			UseImageInput:     command.UseImageInput,
		})
		if err != nil {
			loger.Loger.Warn("[XHH]文本模型理解生图意图失败，使用规则兜底", zap.Error(err))
		} else if !intent.IsImageRequest {
			if !shouldFallbackImageIntent(command, text) {
				return ImageCommentResult{}
			}
			loger.Loger.Warn("[XHH]文本模型未判定为生图，但规则判断为明确生图请求，使用规则兜底", zap.String("reason", intent.Reason))
		} else {
			if strings.TrimSpace(intent.ImagePrompt) != "" {
				prompt = intent.ImagePrompt
				generationPrompt = intent.ImagePrompt
			}
			if command.MentionTargetText == "" {
				command.MentionTargetText = resolveRouteMentionTarget(intent.MentionTarget, "", text)
			}
			command.UsePostContext = command.UsePostContext || intent.NeedsPostContext
			command.UseCommentContext = command.UseCommentContext || intent.NeedsCommentContext
			command.UseImageInput = command.UseImageInput || intent.NeedsImageInput || intent.WantsSimilarImage
		}
	}

	started := time.Now()
	var contents []ai.Content
	if command.UsePostContext || command.UseCommentContext || command.UseImageInput {
		contents, _, _, _ = GetLinkInfo(linkID, rootID, commentID, userID)
	}
	generationPrompt = BuildContextualImagePrompt(generationPrompt, command, contents)
	if command.UseImageInput {
		if len(contents) == 0 {
			loger.Loger.Warn("[XHH]用户要求参考图片，但没有获取到图片内容")
			return ImageCommentResult{}
		}
		if visionText, visionErr := ai.DescribeImagesForGeneration(ctx, contents, generationPrompt); visionErr != nil {
			loger.Loger.Warn("[XHH]图片理解失败，回落普通文本回复", zap.Error(visionErr))
			return ImageCommentResult{}
		} else if strings.TrimSpace(visionText) != "" {
			generationPrompt = generationPrompt + "\n图片理解内容：" + visionText
		}
	}
	if ai.ShouldRefineImagePrompt() {
		refined, err := ai.RefineImagePrompt(ctx, ai.ImagePromptRefineRequest{
			OriginalText:      text,
			RulePrompt:        prompt,
			ContextPrompt:     generationPrompt,
			UsePostContext:    command.UsePostContext,
			UseCommentContext: command.UseCommentContext,
			UseImageInput:     command.UseImageInput,
		})
		if err != nil {
			loger.Loger.Warn("[XHH]文本模型优化生图 prompt 失败，使用规则 prompt", zap.Error(err))
		} else {
			generationPrompt = refined.ImagePrompt
			if command.MentionTargetText == "" {
				command.MentionTargetText = resolveRouteMentionTarget(refined.MentionTarget, "", text)
			}
		}
	}
	loger.Loger.Info("[XHH]开始处理生图评论", zap.Int("comment_id", commentID), zap.Int("link_id", linkID), zap.Int("userid", userID), zap.String("prompt", prompt), zap.Bool("post_context", command.UsePostContext), zap.Bool("comment_context", command.UseCommentContext), zap.Bool("image_input", command.UseImageInput), zap.Bool("prompt_refine", ai.ShouldRefineImagePrompt()), zap.Bool("dry_run", options.DryRun))
	imageResult, err := generateImageForComment(ctx, generationPrompt, options)
	if err != nil {
		return ImageCommentResult{Handled: true, Err: fmt.Errorf("generate image failed: %w", err)}
	}
	loger.Loger.Info("[XHH]生图阶段完成", zap.Int("comment_id", commentID), zap.String("path", imageResult.Path), zap.Int("bytes", len(imageResult.Bytes)), zap.Duration("duration", time.Since(started)))

	imageURL, uploadPlan, err := resolveXHHImageURL(ctx, imageResult, options.DryRun)
	if err != nil {
		return ImageCommentResult{Handled: true, OK: errors.Is(err, ErrMissingXHHCOSCredential), Err: fmt.Errorf("resolve image url failed: %w", err)}
	}
	loger.Loger.Info("[XHH]图片 URL 准备完成", zap.Int("comment_id", commentID), zap.String("image_url", imageURL), zap.String("upload_key", uploadPlan.Key), zap.Bool("uploaded", uploadPlan.Uploaded), zap.Duration("duration", time.Since(started)))

	replyID := "-1"
	rootIDText := "-1"
	replyText := buildImageReplyText(linkID, rootID, commentID, userID, text, options.DryRun)
	if !options.DryRun {
		replyText = prependImageReplyMentions(replyText, linkID, rootID, commentID, userID, options.TriggerUserName, command.MentionTargetText)
	}
	form := CommentCreateFormData(replyText, strconv.Itoa(linkID), replyID, rootIDText, "0", imageURL)

	if options.DryRun {
		printImageDryRun(commentID, linkID, userID, prompt, imageResult, uploadPlan, form)
		return ImageCommentResult{Handled: true, OK: true}
	}

	loger.Loger.Info("[XHH]开始发布带图评论", zap.Int("comment_id", commentID), zap.Int("link_id", linkID), zap.String("reply_id", replyID), zap.String("root_id", rootIDText))
	if ReplyImage(replyText, strconv.Itoa(linkID), replyID, rootIDText, imageURL) {
		loger.Loger.Info("[XHH]带图评论发布完成", zap.Int("comment_id", commentID), zap.Duration("duration", time.Since(started)))
		return ImageCommentResult{Handled: true, OK: true}
	}
	return ImageCommentResult{Handled: true, Err: errors.New("comment/create image reply failed")}
}

func prependImageReplyMentions(replyText string, linkID, rootID, commentID, userID int, triggerUserName, targetText string) string {
	mentions := make([]string, 0, 2)
	if triggerUserName != "" {
		mentions = appendUniqueMention(mentions, buildMention(userID, triggerUserName))
	}
	if targetText != "" {
		mentions = appendUniqueMention(mentions, GetExplicitMentionFromPost(linkID, "艾特"+targetText, userID))
	}
	if len(mentions) == 0 {
		mentions = appendUniqueMention(mentions, GetCommentAuthorMention(linkID, rootID, commentID, userID))
	}
	if len(mentions) == 0 {
		return replyText
	}
	return strings.Join(mentions, "") + replyText
}

func appendUniqueMention(mentions []string, mention string) []string {
	mention = strings.TrimSpace(mention)
	if mention == "" {
		return mentions
	}
	mentionID := extractMentionUserID(mention)
	for _, existing := range mentions {
		if mentionID != "" && mentionID == extractMentionUserID(existing) {
			return mentions
		}
		if mentionID == "" && mention == strings.TrimSpace(existing) {
			return mentions
		}
	}
	return append(mentions, mention+" ")
}

func extractMentionUserID(mention string) string {
	marker := `data-user-id="`
	start := strings.Index(mention, marker)
	if start < 0 {
		return ""
	}
	start += len(marker)
	end := strings.Index(mention[start:], `"`)
	if end < 0 {
		return ""
	}
	return mention[start : start+end]
}

func buildImageReplyText(linkID, rootID, commentID, userID int, originalText string, dryRun bool) string {
	if dryRun {
		return defaultImageReplyText
	}
	contents, topics, tags, _ := GetLinkInfo(linkID, rootID, commentID, userID)
	if len(contents) == 0 {
		return defaultImageReplyText
	}
	contents = appendOwnerContext(contents, userID)
	instruction := "用户请求生成的图片已经成功附在本条评论里。请只输出一句自然简短的中文回复，最多20个字；不要复述用户的图片要求；不要出现“已生成”“prompt”“提示词”“生图指令”。用户原话仅供理解语气：" + originalText
	return normalizeImageReplyText(ai.GetAiReply(contents, instruction, topics, tags, zap.Int("comment_id", commentID), zap.Int("link_id", linkID), zap.Int("user_id", userID), zap.String("question", originalText)))
}

func normalizeImageReplyText(text string) string {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.Join(strings.Fields(text), " ")
	text = strings.Trim(text, "\"'“”‘’「」")
	if text == "" {
		return defaultImageReplyText
	}
	lower := strings.ToLower(text)
	for _, forbidden := range []string{"已生成", "prompt", "提示词", "生图指令"} {
		if strings.Contains(lower, strings.ToLower(forbidden)) {
			return defaultImageReplyText
		}
	}
	runes := []rune(text)
	if len(runes) > maxImageReplyRunes {
		return string(runes[:maxImageReplyRunes])
	}
	return text
}

func generateImageForComment(ctx context.Context, prompt string, options ImageCommentOptions) (ai.ImageResult, error) {
	if options.DryRun && options.MockImage {
		return ai.DryRunImage(prompt), nil
	}
	return ai.GenerateImage(ctx, prompt)
}

func resolveXHHImageURL(ctx context.Context, imageResult ai.ImageResult, dryRun bool) (string, XHHCOSUploadPlan, error) {
	if imageResult.URL != "" && IsXHHCDNImageURL(imageResult.URL) {
		return imageResult.URL, XHHCOSUploadPlan{CDNURL: imageResult.URL, Size: len(imageResult.Bytes)}, nil
	}

	imageBytes := imageResult.Bytes
	if len(imageBytes) == 0 && imageResult.Path != "" {
		data, err := os.ReadFile(imageResult.Path)
		if err != nil {
			return "", XHHCOSUploadPlan{}, err
		}
		imageBytes = data
	}
	if len(imageBytes) == 0 {
		return "", XHHCOSUploadPlan{}, errors.New("generated image has no bytes for upload")
	}

	mode := strings.ToLower(strings.TrimSpace(config.ConfigStruct.Image.UploadMode))
	if mode == "external" || mode == "static" {
		// 临时停用 VPS external/static 图床，统一走小黑盒官方 COS。
		mode = "cos"
	}
	if mode == "" || mode == "xhh_cos" || mode == "xhh-cos" || mode == "cos" {
		plan, err := UploadToXHHCOS(ctx, imageBytes, imageResult.Path, dryRun)
		if err != nil {
			return "", plan, err
		}
		return plan.CDNURL, plan, nil
	}
	return "", XHHCOSUploadPlan{}, fmt.Errorf("unsupported image.uploadMode: %s", config.ConfigStruct.Image.UploadMode)
}

func printImageDryRun(commentID, linkID, userID int, prompt string, imageResult ai.ImageResult, uploadPlan XHHCOSUploadPlan, form mapLikeValues) {
	fmt.Println("dry-run image comment flow")
	fmt.Printf("trigger_comment_id=%d\n", commentID)
	fmt.Printf("link_id=%d\n", linkID)
	fmt.Printf("trigger_userid=%d\n", userID)
	fmt.Printf("prompt=%s\n", prompt)
	fmt.Printf("generated_image_path=%s\n", imageResult.Path)
	fmt.Printf("generated_image_bytes=%d\n", len(imageResult.Bytes))
	fmt.Printf("planned_cos_key=%s\n", uploadPlan.Key)
	fmt.Printf("planned_cdn_url=%s\n", uploadPlan.CDNURL)
	fmt.Println("comment/create Form Data:")
	for _, key := range []string{"is_cy", "link_id", "reply_id", "root_id", "text", "imgs"} {
		fmt.Printf("%s=%s\n", key, form.Get(key))
	}
}

type mapLikeValues interface {
	Get(string) string
}
