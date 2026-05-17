package xhh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"xhhrobot/config"
)

const xhhCOSBucket = "imgheybox-1251007209"
const xhhCOSRegion = "ap-shanghai"
const xhhCDNHost = "imgheybox.max-c.com"
const xhhCOSUploadTokenPath = "/bbs/app/api/qcloud/cos/upload/token/v2"

var ErrMissingXHHCOSCredential = errors.New("missing XHH COS upload credential/provider")

type XHHCOSUploadPlan struct {
	Key       string
	UploadURL string
	CDNURL    string
	Size      int
	DryRun    bool
	Uploaded  bool
}

type xhhCOSUploadTokenResp struct {
	Status string `json:"status"`
	Msg    string `json:"msg"`
	Result struct {
		Keys   []string `json:"keys"`
		Region string   `json:"region"`
		Bucket string   `json:"bucket"`
		Host   string   `json:"host"`
	} `json:"result"`
}

func IsXHHCDNImageURL(imageURL string) bool {
	return strings.HasPrefix(imageURL, "https://"+xhhCDNHost+"/")
}

func PlanXHHCOSUpload(imageBytes []byte, sourcePath string, now time.Time) XHHCOSUploadPlan {
	ext := imageExtFromBytesOrPath(imageBytes, sourcePath)
	sum := sha256.Sum256(append(imageBytes, []byte(now.Format(time.RFC3339Nano))...))
	filename := hex.EncodeToString(sum[:])[:32] + ext
	key := "/web/bbs/" + now.Format("2006/01/02") + "/" + filename
	return buildXHHCOSUploadPlan(xhhCOSBucket, xhhCOSRegion, xhhCDNHost, key, len(imageBytes))
}

func UploadToXHHCOS(ctx context.Context, imageBytes []byte, sourcePath string, dryRun bool) (XHHCOSUploadPlan, error) {
	plan := PlanXHHCOSUpload(imageBytes, sourcePath, time.Now())
	plan.DryRun = dryRun
	if dryRun {
		return plan, nil
	}

	mimeType := imageMimeType(imageBytes)
	tokenPlan, authorization, securityToken, err := requestXHHCOSUploadToken(plan.Key, mimeType, len(imageBytes))
	if err != nil {
		return plan, err
	}
	plan = tokenPlan
	plan.Size = len(imageBytes)

	if err := putXHHCOSObject(ctx, plan.UploadURL, mimeType, imageBytes, authorization, securityToken); err != nil {
		return plan, err
	}
	plan.Uploaded = true
	return plan, nil
}

func requestXHHCOSUploadToken(key, mimeType string, size int) (XHHCOSUploadPlan, string, string, error) {
	keys, err := json.Marshal([]string{key})
	if err != nil {
		return XHHCOSUploadPlan{}, "", "", err
	}
	mimetypes, err := json.Marshal([]string{mimeType})
	if err != nil {
		return XHHCOSUploadPlan{}, "", "", err
	}

	form := url.Values{}
	form.Set("bucket", xhhCOSBucket)
	form.Set("keys", string(keys))
	form.Set("mimetypes", string(mimetypes))
	form.Set("is_multipart_upload", "0")

	resp, err := sendXHHCOSUploadTokenReq(strings.NewReader(form.Encode()))
	if err != nil {
		return XHHCOSUploadPlan{}, "", "", err
	}
	if resp == nil {
		return XHHCOSUploadPlan{}, "", "", errors.New("XHH COS upload token request failed")
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return XHHCOSUploadPlan{}, "", "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return XHHCOSUploadPlan{}, "", "", fmt.Errorf("XHH COS upload token request failed: status=%d", resp.StatusCode)
	}

	var parsed xhhCOSUploadTokenResp
	if err := json.Unmarshal(data, &parsed); err != nil {
		return XHHCOSUploadPlan{}, "", "", err
	}
	if parsed.Status != "ok" {
		if parsed.Msg != "" {
			return XHHCOSUploadPlan{}, "", "", errors.New(parsed.Msg)
		}
		return XHHCOSUploadPlan{}, "", "", fmt.Errorf("XHH COS upload token status=%s", parsed.Status)
	}
	if len(parsed.Result.Keys) == 0 || parsed.Result.Bucket == "" || parsed.Result.Region == "" || parsed.Result.Host == "" {
		return XHHCOSUploadPlan{}, "", "", errors.New("XHH COS upload token response missing key/bucket/region/host")
	}

	authorization, securityToken := extractXHHCOSUploadCredential(resp.Header, data)
	if authorization == "" || securityToken == "" {
		return XHHCOSUploadPlan{}, "", "", ErrMissingXHHCOSCredential
	}

	plan := buildXHHCOSUploadPlan(parsed.Result.Bucket, parsed.Result.Region, parsed.Result.Host, parsed.Result.Keys[0], size)
	return plan, authorization, securityToken, nil
}

func sendXHHCOSUploadTokenReq(body io.Reader) (*http.Response, error) {
	cfg := config.ConfigStruct.Xhh
	u, err := url.Parse(cfg.BaseUrl + xhhCOSUploadTokenPath)
	if err != nil {
		return nil, err
	}

	hkey, nonce, requestTime := GetKeys(xhhCOSUploadTokenPath)
	query := u.Query()
	query.Set("app", "heybox")
	query.Set("os_type", "web")
	query.Set("x_app", "heybox")
	query.Set("x_client_type", "web")
	query.Set("x_os_type", "Mac")
	query.Set("x_request_default", "true")
	query.Set("x_client_version", "999.999.999")
	query.Set("version", "999.0.4")
	if cfg.DeviceID != "" {
		query.Set("device_id", cfg.DeviceID)
	}
	if Info.HeyBoxId != "" {
		query.Set("heybox_id", Info.HeyBoxId)
	}
	query.Set("hkey", hkey)
	query.Set("_time", strconv.Itoa(requestTime))
	query.Set("nonce", nonce)
	u.RawQuery = query.Encode()

	req, err := http.NewRequest("POST", u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	req.Header.Set("Origin", "https://www.xiaoheihe.cn")
	req.Header.Set("Referer", "https://www.xiaoheihe.cn/")
	req.Header.Set("Host", "api.xiaoheihe.cn")
	if Info.Cookie != "" {
		req.Header.Set("cookie", Info.Cookie)
	}

	return http.DefaultClient.Do(req)
}

func extractXHHCOSUploadCredential(headers http.Header, data []byte) (string, string) {
	authorization := strings.TrimSpace(headers.Get("Authorization"))
	securityToken := strings.TrimSpace(headers.Get("x-cos-security-token"))

	var raw any
	if err := json.Unmarshal(data, &raw); err == nil {
		if authorization == "" {
			authorization = findStringByNormalizedKey(raw, map[string]bool{"authorization": true})
		}
		if securityToken == "" {
			securityToken = findStringByNormalizedKey(raw, map[string]bool{
				"xcossecuritytoken": true,
				"securitytoken":     true,
				"sessiontoken":      true,
			})
		}
	}

	return authorization, securityToken
}

func findStringByNormalizedKey(data any, candidates map[string]bool) string {
	switch value := data.(type) {
	case map[string]any:
		for key, item := range value {
			if candidates[normalizeCOSCredentialKey(key)] {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
		for _, item := range value {
			if found := findStringByNormalizedKey(item, candidates); found != "" {
				return found
			}
		}
	case []any:
		for _, item := range value {
			if found := findStringByNormalizedKey(item, candidates); found != "" {
				return found
			}
		}
	}
	return ""
}

func normalizeCOSCredentialKey(key string) string {
	key = strings.ToLower(key)
	replacer := strings.NewReplacer("-", "", "_", "", " ", "")
	return replacer.Replace(key)
}

func putXHHCOSObject(ctx context.Context, uploadURL, mimeType string, imageBytes []byte, authorization, securityToken string) error {
	if authorization == "" || securityToken == "" {
		return ErrMissingXHHCOSCredential
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", uploadURL, bytes.NewReader(imageBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("x-cos-security-token", securityToken)
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("Origin", "https://www.xiaoheihe.cn")
	req.Header.Set("Referer", "https://www.xiaoheihe.cn/")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("XHH COS PUT failed: status=%d body=%s", resp.StatusCode, limitCOSString(string(data), 300))
	}
	return nil
}

func buildXHHCOSUploadPlan(bucket, region, host, key string, size int) XHHCOSUploadPlan {
	key = ensureLeadingSlash(key)
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	return XHHCOSUploadPlan{
		Key:       key,
		UploadURL: "https://" + bucket + ".cos." + region + ".myqcloud.com" + key,
		CDNURL:    "https://" + host + key,
		Size:      size,
	}
}

func ensureLeadingSlash(key string) string {
	if strings.HasPrefix(key, "/") {
		return key
	}
	return "/" + key
}

func imageExtFromBytesOrPath(imageBytes []byte, sourcePath string) string {
	contentType := http.DetectContentType(imageBytes)
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/png":
		return ".png"
	}
	ext := strings.ToLower(filepath.Ext(sourcePath))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp":
		return ext
	default:
		return ".png"
	}
}

func imageMimeType(imageBytes []byte) string {
	contentType := http.DetectContentType(imageBytes)
	switch contentType {
	case "image/jpeg", "image/webp", "image/png":
		return contentType
	default:
		return "image/png"
	}
}

func limitCOSString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
