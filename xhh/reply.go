package xhh

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"xhhrobot/loger"
)

func Reply(text, link_id, reply_id, root_id, iscy string) {
	Path := "/bbs/app/comment/create"
	Body := fmt.Sprintf("is_cy=%s&link_id=%s&reply_id=%s&root_id=%s&text=%s", iscy, link_id, reply_id, root_id, url.QueryEscape(text))
	resp := SendReq("POST", Path, bytes.NewReader([]byte(Body)), "")

	_, err := io.ReadAll(resp.Body)
	if err != nil {
		loger.Loger.Error("[XHH]无法解析Body")
	}
}
