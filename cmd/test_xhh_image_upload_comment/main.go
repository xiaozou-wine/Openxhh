package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"
	"xhhrobot/config"
	"xhhrobot/loger"
	"xhhrobot/xhh"
)

func main() {
	filePath := flag.String("file", "", "local image file path")
	linkID := flag.String("link_id", "", "link id")
	replyID := flag.String("reply_id", "-1", "reply comment id")
	rootID := flag.String("root_id", "-1", "root comment id")
	text := flag.String("text", "图片测试", "comment text")
	publish := flag.Bool("publish", false, "upload image and publish comment")
	flag.Parse()

	if *filePath == "" || *linkID == "" {
		fmt.Println("usage: go run ./cmd/test_xhh_image_upload_comment -file ./image.png -link_id 181099114 [-reply_id -1] [-root_id -1] [-text 图片测试] [-publish=true]")
		os.Exit(1)
	}

	imageBytes, err := os.ReadFile(*filePath)
	if err != nil {
		fmt.Println("read image failed:", err)
		os.Exit(1)
	}

	loger.InitLog()
	config.InitConfig()
	xhh.Init()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	plan, err := xhh.UploadToXHHCOS(ctx, imageBytes, *filePath, !*publish)
	if err != nil {
		fmt.Println("upload failed:", err)
		os.Exit(1)
	}

	form := xhh.CommentCreateFormData(*text, *linkID, *replyID, *rootID, "0", plan.CDNURL)
	fmt.Println("XHH image upload comment flow")
	fmt.Printf("publish=%v\n", *publish)
	fmt.Printf("image_file=%s\n", *filePath)
	fmt.Printf("image_bytes=%d\n", len(imageBytes))
	fmt.Printf("cos_key=%s\n", plan.Key)
	fmt.Printf("cos_uploaded=%v\n", plan.Uploaded)
	fmt.Printf("cdn_url=%s\n", plan.CDNURL)
	fmt.Println("comment/create Form Data:")
	for _, key := range []string{"is_cy", "link_id", "reply_id", "root_id", "text", "imgs"} {
		fmt.Printf("%s=%s\n", key, form.Get(key))
	}

	if !*publish {
		fmt.Println("dry-run only; add -publish=true to upload and post the image comment")
		return
	}

	if !xhh.ReplyImage(*text, *linkID, *replyID, *rootID, plan.CDNURL) {
		fmt.Println("comment publish failed")
		os.Exit(1)
	}
	fmt.Println("comment publish ok")
}
