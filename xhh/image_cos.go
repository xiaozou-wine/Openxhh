package xhh

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"openxhh/config"
	"openxhh/loger"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	xhhCOSBucket             = "imgheybox-1251007209"
	xhhCOSRegion             = "ap-shanghai"
	xhhCDNHost               = "imgheybox.max-c.com"
	xhhCOSUploadInfoPath     = "/bbs/app/api/qcloud/cos/upload/info/v2"
	xhhCOSUploadTokenPath    = "/bbs/app/api/qcloud/cos/upload/token/v2"
	xhhCOSUploadCallbackPath = "/bbs/app/api/qcloud/cos/upload/callback/v2"
	xhhCOSWebQuery           = "app=heybox&os_type=web&x_app=heybox&x_client_type=web&x_os_type=Mac&x_client_version=999.999.999&x_request_default=true"
)

type XHHCOSUploadPlan struct {
	Key       string
	UploadURL string
	CDNURL    string
	Size      int
	DryRun    bool
	Uploaded  bool
}

type xhhCOSUploadInfo struct {
	Bucket string
	Region string
	Host   string
	Key    string
}

type xhhCOSUploadCredential struct {
	TmpSecretID  string
	TmpSecretKey string
	SessionToken string
	StartTime    int64
	ExpiredTime  int64
}

type xhhCOSUploadInfoResp struct {
	Status string `json:"status"`
	Msg    string `json:"msg"`
	Result struct {
		Keys   []string `json:"keys"`
		Region string   `json:"region"`
		Bucket string   `json:"bucket"`
		Host   string   `json:"host"`
	} `json:"result"`
}

type xhhCOSUploadTokenResp struct {
	Status string `json:"status"`
	Msg    string `json:"msg"`
	Result struct {
		Credentials struct {
			TmpSecretID  string `json:"tmpSecretId"`
			TmpSecretKey string `json:"tmpSecretKey"`
			SessionToken string `json:"sessionToken"`
		} `json:"credentials"`
		StartTime   int64 `json:"startTime"`
		ExpiredTime int64 `json:"expiredTime"`
	} `json:"result"`
}

type xhhCOSUploadCallbackResp struct {
	Status string `json:"status"`
	Msg    string `json:"msg"`
	Result struct {
		PreviewURLs []string `json:"preview_urls"`
		Thumbs      []string `json:"thumbs"`
	} `json:"result"`
}

type xhhCOSUploadFileInfo struct {
	Name     string  `json:"name"`
	MimeType string  `json:"mimetype"`
	FSize    int     `json:"fsize"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	Duration float64 `json:"duration,omitempty"`
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
	if len(imageBytes) == 0 {
		return XHHCOSUploadPlan{}, errors.New("image bytes are required")
	}
	if dryRun {
		plan := PlanXHHCOSUpload(imageBytes, sourcePath, time.Now())
		plan.DryRun = true
		return plan, nil
	}

	mimeType := imageMimeType(imageBytes)
	info, err := requestXHHCOSUploadInfo(ctx, imageBytes, sourcePath, mimeType)
	if err != nil {
		return XHHCOSUploadPlan{}, err
	}
	credential, err := requestXHHCOSUploadToken(ctx, info.Bucket, info.Key, mimeType)
	if err != nil {
		return XHHCOSUploadPlan{}, err
	}

	plan := buildXHHCOSUploadPlan(info.Bucket, info.Region, info.Host, info.Key, len(imageBytes))
	if err := putXHHCOSObject(ctx, plan.UploadURL, info.Bucket, info.Key, imageBytes, mimeType, credential); err != nil {
		return plan, err
	}
	plan.Uploaded = true

	previewURL, err := callbackXHHCOSUpload(ctx, info.Key)
	if err != nil {
		return plan, err
	}
	if previewURL != "" {
		plan.CDNURL = previewURL
	}
	return plan, nil
}

func requestXHHCOSUploadInfo(ctx context.Context, imageBytes []byte, sourcePath, mimeType string) (xhhCOSUploadInfo, error) {
	fileInfo := xhhCOSUploadFileInfo{
		Name:     xhhCOSUploadFileName(sourcePath, imageBytes),
		MimeType: mimeType,
		FSize:    len(imageBytes),
	}
	if width, height := imageDimensions(imageBytes); width > 0 && height > 0 {
		fileInfo.Width = width
		fileInfo.Height = height
	}
	fileInfos, err := json.Marshal([]xhhCOSUploadFileInfo{fileInfo})
	if err != nil {
		return xhhCOSUploadInfo{}, err
	}
	body := "file_infos=" + url.QueryEscape(string(fileInfos)) + "&scope=bbs&need_cache=0"
	data, err := postXHHCOSAPI(ctx, xhhCOSUploadInfoPath, "", body)
	if err != nil {
		return xhhCOSUploadInfo{}, err
	}

	var parsed xhhCOSUploadInfoResp
	if err := json.Unmarshal(data, &parsed); err != nil {
		return xhhCOSUploadInfo{}, err
	}
	if parsed.Status != "ok" {
		return xhhCOSUploadInfo{}, xhhCOSStatusError("XHH COS upload info", parsed.Status, parsed.Msg)
	}
	if len(parsed.Result.Keys) == 0 || parsed.Result.Bucket == "" || parsed.Result.Host == "" {
		return xhhCOSUploadInfo{}, errors.New("XHH COS upload info response missing key/bucket/host")
	}
	region := strings.TrimSpace(parsed.Result.Region)
	if region == "" {
		region = xhhCOSRegion
	}
	return xhhCOSUploadInfo{
		Bucket: parsed.Result.Bucket,
		Region: region,
		Host:   parsed.Result.Host,
		Key:    ensureLeadingSlash(parsed.Result.Keys[0]),
	}, nil
}

func requestXHHCOSUploadToken(ctx context.Context, bucket, key, mimeType string) (xhhCOSUploadCredential, error) {
	keys, err := json.Marshal([]string{ensureLeadingSlash(key)})
	if err != nil {
		return xhhCOSUploadCredential{}, err
	}
	mimetypes, err := json.Marshal([]string{mimeType})
	if err != nil {
		return xhhCOSUploadCredential{}, err
	}
	body := "bucket=" + url.QueryEscape(bucket) +
		"&keys=" + url.QueryEscape(string(keys)) +
		"&mimetypes=" + url.QueryEscape(string(mimetypes)) +
		"&is_multipart_upload=0"
	data, err := postXHHCOSAPI(ctx, xhhCOSUploadTokenPath, "", body)
	if err != nil {
		return xhhCOSUploadCredential{}, err
	}

	var parsed xhhCOSUploadTokenResp
	if err := json.Unmarshal(data, &parsed); err != nil {
		return xhhCOSUploadCredential{}, err
	}
	if parsed.Status != "ok" {
		return xhhCOSUploadCredential{}, xhhCOSStatusError("XHH COS upload token", parsed.Status, parsed.Msg)
	}
	credential := xhhCOSUploadCredential{
		TmpSecretID:  parsed.Result.Credentials.TmpSecretID,
		TmpSecretKey: parsed.Result.Credentials.TmpSecretKey,
		SessionToken: parsed.Result.Credentials.SessionToken,
		StartTime:    parsed.Result.StartTime,
		ExpiredTime:  parsed.Result.ExpiredTime,
	}
	if credential.TmpSecretID == "" || credential.TmpSecretKey == "" || credential.SessionToken == "" || credential.StartTime == 0 || credential.ExpiredTime == 0 {
		return xhhCOSUploadCredential{}, errors.New("XHH COS upload token response missing STS credential")
	}
	return credential, nil
}

func putXHHCOSObject(ctx context.Context, uploadURL, bucket, key string, imageBytes []byte, mimeType string, credential xhhCOSUploadCredential) error {
	key = ensureLeadingSlash(key)
	query := map[string]string{
		"bucket":   bucket,
		"keys":     jsonStrings([]string{key}),
		"mimetype": mimeType,
	}
	headers := map[string]string{
		"content-type":         mimeType,
		"host":                 xhhCOSUploadHost(uploadURL),
		"x-cos-security-token": credential.SessionToken,
	}
	queryString := sortedCOSPairs(query)
	authorization := buildXHHCOSAuthorization("put", key, query, headers, credential)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL+"?"+queryString, bytes.NewReader(imageBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("x-cos-security-token", credential.SessionToken)

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

func callbackXHHCOSUpload(ctx context.Context, key string) (string, error) {
	keys, err := json.Marshal([]string{ensureLeadingSlash(key)})
	if err != nil {
		return "", err
	}
	data, err := postXHHCOSAPI(ctx, xhhCOSUploadCallbackPath, "&is_finished=true", "keys="+url.QueryEscape(string(keys)))
	if err != nil {
		return "", err
	}
	var parsed xhhCOSUploadCallbackResp
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	if parsed.Status != "ok" {
		return "", xhhCOSStatusError("XHH COS upload callback", parsed.Status, parsed.Msg)
	}
	loger.Loger.Debug("[XHH]COS callback 响应", zap.Strings("preview_urls", parsed.Result.PreviewURLs), zap.Strings("thumbs", parsed.Result.Thumbs))
	if len(parsed.Result.PreviewURLs) > 0 {
		return normalizeXHHCOSCallbackURL(parsed.Result.PreviewURLs[0]), nil
	}
	if len(parsed.Result.Thumbs) > 0 {
		return normalizeXHHCOSCallbackURL(parsed.Result.Thumbs[0]), nil
	}
	return "", nil
}

// normalizeXHHCOSCallbackURL 将回调返回的 COS 直连 URL 转换为 CDN URL
// COS bucket 没有公有读权限，必须走 CDN 域名才能访问
func normalizeXHHCOSCallbackURL(rawURL string) string {
	cosHost := xhhCOSBucket + ".cos." + xhhCOSRegion + ".myqcloud.com"
	if strings.Contains(rawURL, cosHost) {
		return strings.Replace(rawURL, "https://"+cosHost, "https://"+xhhCDNHost, 1)
	}
	return rawURL
}

func postXHHCOSAPI(ctx context.Context, path, extraQuery, body string) ([]byte, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.ConfigStruct.Xhh.BaseUrl), "/")
	if baseURL == "" {
		baseURL = "https://api.xiaoheihe.cn"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path+"?"+xhhCOSWebQuery+extraQuery, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	req.Header.Set("Origin", "https://www.xiaoheihe.cn")
	req.Header.Set("Referer", "https://www.xiaoheihe.cn/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	if Info.Cookie != "" {
		req.Header.Set("cookie", Info.Cookie)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("XHH COS API failed: path=%s status=%d body=%s", path, resp.StatusCode, limitCOSString(string(data), 300))
	}
	return data, nil
}

func buildXHHCOSAuthorization(method, key string, query, headers map[string]string, credential xhhCOSUploadCredential) string {
	keyTime := fmt.Sprintf("%d;%d", credential.StartTime, credential.ExpiredTime)
	formatString := strings.ToLower(method) + "\n" + ensureLeadingSlash(key) + "\n" + sortedCOSPairs(query) + "\n" + sortedCOSPairs(headers) + "\n"
	stringToSign := "sha1\n" + keyTime + "\n" + sha1Hex(formatString) + "\n"
	signKey := hmacSHA1Hex(credential.TmpSecretKey, keyTime)
	signature := hmacSHA1Hex(signKey, stringToSign)
	return "q-sign-algorithm=sha1" +
		"&q-ak=" + credential.TmpSecretID +
		"&q-sign-time=" + keyTime +
		"&q-key-time=" + keyTime +
		"&q-header-list=" + strings.Join(sortedKeys(headers), ";") +
		"&q-url-param-list=" + strings.Join(sortedKeys(query), ";") +
		"&q-signature=" + signature
}

func sortedCOSPairs(values map[string]string) string {
	keys := sortedKeys(values)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, cosEscape(key)+"="+cosEscape(values[key]))
	}
	return strings.Join(pairs, "&")
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, strings.ToLower(key))
	}
	sort.Strings(keys)
	return keys
}

func cosEscape(value string) string {
	const hexDigits = "0123456789ABCDEF"
	var builder strings.Builder
	for _, ch := range []byte(value) {
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			builder.WriteByte(ch)
			continue
		}
		builder.WriteByte('%')
		builder.WriteByte(hexDigits[ch>>4])
		builder.WriteByte(hexDigits[ch&0x0f])
	}
	return builder.String()
}

func sha1Hex(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func hmacSHA1Hex(secret, value string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
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

func xhhCOSUploadFileName(sourcePath string, imageBytes []byte) string {
	name := filepath.Base(strings.TrimSpace(sourcePath))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "generated" + imageExtFromBytesOrPath(imageBytes, sourcePath)
	}
	return name
}

func imageDimensions(imageBytes []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(imageBytes))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func jsonStrings(values []string) string {
	data, _ := json.Marshal(values)
	return string(data)
}

func xhhCOSUploadHost(uploadURL string) string {
	parsed, err := url.Parse(uploadURL)
	if err != nil {
		return ""
	}
	return parsed.Host
}

func xhhCOSStatusError(action, status, msg string) error {
	if msg != "" {
		return fmt.Errorf("%s failed: status=%s msg=%s", action, status, msg)
	}
	return fmt.Errorf("%s failed: status=%s", action, status)
}

func limitCOSString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
