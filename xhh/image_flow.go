package xhh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"xhhrobot/ai"
	"xhhrobot/config"
	"xhhrobot/loger"

	"go.uber.org/zap"
)

type ImageCommentOptions struct {
	DryRun    bool
	MockImage bool
}

type ImageCommentResult struct {
	Handled bool
	OK      bool
	Err     error
}

func HandleImageGenerationComment(linkID, commentID, rootID, userID int, text string) (bool, bool) {
	result := ProcessImageGenerationComment(linkID, commentID, rootID, userID, text, ImageCommentOptions{})
	if result.Err != nil {
		loger.Loger.Error("[XHH]图片评论处理失败", zap.Error(result.Err), zap.Int("comment_id", commentID), zap.Int("link_id", linkID))
		if result.Handled {
			return true, true
		}
	}
	return result.Handled, result.OK
}

func ProcessImageGenerationComment(linkID, commentID, rootID, userID int, text string, options ImageCommentOptions) ImageCommentResult {
	prompt, ok := ExtractImagePrompt(text)
	if !ok {
		return ImageCommentResult{}
	}
	if !Check(userID) {
		if options.DryRun {
			fmt.Printf("dry-run: unauthorized user ignored, comment_id=%d userid=%d\n", commentID, userID)
		}
		return ImageCommentResult{Handled: true, OK: true}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	started := time.Now()
	loger.Loger.Info("[XHH]开始处理生图评论", zap.Int("comment_id", commentID), zap.Int("link_id", linkID), zap.Int("userid", userID), zap.String("prompt", prompt), zap.Bool("dry_run", options.DryRun))
	imageResult, err := generateImageForComment(ctx, prompt, options)
	if err != nil {
		return ImageCommentResult{Handled: true, Err: fmt.Errorf("generate image failed: %w", err)}
	}
	loger.Loger.Info("[XHH]生图阶段完成", zap.Int("comment_id", commentID), zap.String("path", imageResult.Path), zap.Int("bytes", len(imageResult.Bytes)), zap.Duration("duration", time.Since(started)))

	imageURL, uploadPlan, err := resolveXHHImageURL(ctx, imageResult, options.DryRun)
	if err != nil {
		return ImageCommentResult{Handled: true, OK: errors.Is(err, ErrMissingXHHCOSCredential), Err: fmt.Errorf("resolve image url failed: %w", err)}
	}
	loger.Loger.Info("[XHH]图片 URL 准备完成", zap.Int("comment_id", commentID), zap.String("image_url", imageURL), zap.String("upload_key", uploadPlan.Key), zap.Bool("uploaded", uploadPlan.Uploaded), zap.Duration("duration", time.Since(started)))

	replyID := strconv.Itoa(commentID)
	replyRootID := rootID
	if replyRootID <= 0 {
		replyRootID = commentID
	}
	rootIDText := strconv.Itoa(replyRootID)
	replyText := "已生成：" + prompt
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
	if mode == "" || mode == "xhh_cos" || mode == "xhh-cos" || mode == "cos" {
		plan, err := UploadToXHHCOS(ctx, imageBytes, imageResult.Path, dryRun)
		if err != nil {
			return "", plan, err
		}
		return plan.CDNURL, plan, nil
	}
	if mode == "external" || mode == "static" {
		plan, err := UploadToExternalImageHost(imageBytes, imageResult.Path, dryRun)
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
