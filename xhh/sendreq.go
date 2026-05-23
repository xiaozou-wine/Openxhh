package xhh

import (
	"io"
	"net/http"
	"net/url"
	"openxhh/config"
	"openxhh/loger"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

var sendReqMu sync.Mutex
var lastSendReqTime time.Time

func minSendReqInterval() time.Duration {
	interval := config.ConfigStruct.Xhh.MinRequestInterval
	if interval <= 0 {
		return 2 * time.Second
	}
	return time.Duration(interval) * time.Second
}

func SendReq(Method, Path string, Body io.Reader, other string) *http.Response {
	sendReqMu.Lock()
	defer sendReqMu.Unlock()

	if !lastSendReqTime.IsZero() {
		elapsed := time.Since(lastSendReqTime)
		if wait := minSendReqInterval() - elapsed; wait > 0 {
			time.Sleep(wait)
		}
	}
	lastSendReqTime = time.Now()

	cfg := config.ConfigStruct.Xhh
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseUrl), "/")
	if baseURL == "" {
		baseURL = "https://api.xiaoheihe.cn"
	}
	version := strings.TrimSpace(cfg.Ver)
	if version == "" {
		version = "999.0.4"
	}
	webVersion := strings.TrimSpace(cfg.WebVer)
	if webVersion == "" {
		webVersion = "2.5"
	}
	u, err := url.Parse(baseURL + Path + other)
	if err != nil {
		loger.Loger.Error("[SendReq]Creat requset url failed")
		return nil
	}
	reqUrl := u.Query()
	hkey, nonce, reqTime := GetKeys(Path)
	reqUrl.Set("os_type", "web")
	reqUrl.Set("app", "web")
	reqUrl.Set("client_type", "web")
	reqUrl.Set("version", version)
	reqUrl.Set("web_version", webVersion)
	reqUrl.Set("x_client_type", "web")
	reqUrl.Set("x_app", "heybox_website")
	if Info.HeyBoxId != "" {
		reqUrl.Set("heybox_id", Info.HeyBoxId)
	}
	reqUrl.Set("x_os_type", "Windows")
	reqUrl.Set("device_info", "Chrome")
	reqUrl.Set("device_id", cfg.DeviceID)
	reqUrl.Set("hkey", hkey)
	reqUrl.Set("_time", strconv.Itoa(reqTime))
	reqUrl.Set("nonce", nonce)
	reqUrl.Set("_notip", "true")
	u.RawQuery = reqUrl.Encode()
	req, err := http.NewRequest(Method, u.String(), Body)
	if err != nil {
		loger.Loger.Error("[SendReq]Can't Create HttpReq", zap.Error(err))
		return nil
	}
	req.Header.Set("host", "api.xiaoheihe.cn")
	if Info.Cookie != "" {
		req.Header.Set("cookie", Info.Cookie)
	}
	req.Header.Set("Referer", "https://www.xiaoheihe.cn/")
	if Body != nil {
		req.Header.Set("content-type", "application/x-www-form-urlencoded;charset=utf-8")
	}
	client := http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		loger.Loger.Error("[XHH] SendReq Failed", zap.Error(err))
	}
	return resp
}
