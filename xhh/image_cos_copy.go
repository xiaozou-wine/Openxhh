package xhh

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
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
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	xhhCOSUploadInfoPathOfficial     = "/bbs/app/api/qcloud/cos/upload/info/v2"
	xhhCOSUploadCallbackPathOfficial = "/bbs/app/api/qcloud/cos/upload/callback/v2"
	xhhCOSOfficialWebQuery           = "app=heybox&os_type=web&x_app=heybox&x_client_type=web&x_os_type=Mac&x_client_version=999.999.999&x_request_default=true"
)

type xhhCOSUploadInfoOfficial struct {
	Bucket string
	Region string
	Host   string
	Key    string
}

type xhhCOSUploadCredentialOfficial struct {
	TmpSecretID  string
	TmpSecretKey string
	SessionToken string
	StartTime    int64
	ExpiredTime  int64
}

type xhhCOSUploadInfoRespOfficial struct {
	Status string `json:"status"`
	Msg    string `json:"msg"`
	Result struct {
		Keys   []string `json:"keys"`
		Region string   `json:"region"`
		Bucket string   `json:"bucket"`
		Host   string   `json:"host"`
	} `json:"result"`
}

type xhhCOSUploadTokenRespOfficial struct {
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

type xhhCOSUploadCallbackRespOfficial struct {
	Status string `json:"status"`
	Msg    string `json:"msg"`
	Result struct {
		PreviewURLs []string `json:"preview_urls"`
		Thumbs      []string `json:"thumbs"`
	} `json:"result"`
}

type xhhCOSUploadFileInfoOfficial struct {
	Name     string  `json:"name"`
	MimeType string  `json:"mimetype"`
	FSize    int     `json:"fsize"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	Duration float64 `json:"duration,omitempty"`
}

func UploadToXHHCOSOfficial(ctx context.Context, imageBytes []byte, sourcePath string, dryRun bool) (XHHCOSUploadPlan, error) {
	if len(imageBytes) == 0 {
		return XHHCOSUploadPlan{}, errors.New("image bytes are required")
	}
	if dryRun {
		plan := PlanXHHCOSUpload(imageBytes, sourcePath, time.Now())
		plan.DryRun = true
		return plan, nil
	}

	mimeType := imageMimeType(imageBytes)
	info, err := requestXHHCOSUploadInfoOfficial(ctx, imageBytes, sourcePath, mimeType)
	if err != nil {
		return XHHCOSUploadPlan{}, err
	}
	credential, err := requestXHHCOSUploadTokenOfficial(ctx, info.Bucket, info.Key, mimeType)
	if err != nil {
		return XHHCOSUploadPlan{}, err
	}

	plan := buildXHHCOSUploadPlan(info.Bucket, info.Region, info.Host, info.Key, len(imageBytes))
	if err := putXHHCOSObjectOfficial(ctx, plan.UploadURL, info.Bucket, info.Key, imageBytes, mimeType, credential); err != nil {
		return plan, err
	}
	plan.Uploaded = true

	previewURL, err := callbackXHHCOSUploadOfficial(ctx, info.Key)
	if err != nil {
		return plan, err
	}
	if previewURL != "" {
		plan.CDNURL = previewURL
	}
	return plan, nil
}

func requestXHHCOSUploadInfoOfficial(ctx context.Context, imageBytes []byte, sourcePath, mimeType string) (xhhCOSUploadInfoOfficial, error) {
	fileInfo := xhhCOSUploadFileInfoOfficial{
		Name:     xhhCOSUploadFileNameOfficial(sourcePath, imageBytes),
		MimeType: mimeType,
		FSize:    len(imageBytes),
	}
	if width, height := imageDimensionsOfficial(imageBytes); width > 0 && height > 0 {
		fileInfo.Width = width
		fileInfo.Height = height
	}
	fileInfos, err := json.Marshal([]xhhCOSUploadFileInfoOfficial{fileInfo})
	if err != nil {
		return xhhCOSUploadInfoOfficial{}, err
	}
	body := "file_infos=" + url.QueryEscape(string(fileInfos)) + "&scope=bbs&need_cache=0"
	data, err := postXHHOfficialCOS(ctx, xhhCOSUploadInfoPathOfficial, "", body)
	if err != nil {
		return xhhCOSUploadInfoOfficial{}, err
	}

	var parsed xhhCOSUploadInfoRespOfficial
	if err := json.Unmarshal(data, &parsed); err != nil {
		return xhhCOSUploadInfoOfficial{}, err
	}
	if parsed.Status != "ok" {
		return xhhCOSUploadInfoOfficial{}, xhhCOSStatusErrorOfficial("XHH COS upload info", parsed.Status, parsed.Msg)
	}
	if len(parsed.Result.Keys) == 0 || parsed.Result.Bucket == "" || parsed.Result.Host == "" {
		return xhhCOSUploadInfoOfficial{}, errors.New("XHH COS upload info response missing key/bucket/host")
	}
	region := strings.TrimSpace(parsed.Result.Region)
	if region == "" {
		region = xhhCOSRegion
	}
	return xhhCOSUploadInfoOfficial{
		Bucket: parsed.Result.Bucket,
		Region: region,
		Host:   parsed.Result.Host,
		Key:    ensureLeadingSlash(parsed.Result.Keys[0]),
	}, nil
}

func requestXHHCOSUploadTokenOfficial(ctx context.Context, bucket, key, mimeType string) (xhhCOSUploadCredentialOfficial, error) {
	keys, err := json.Marshal([]string{ensureLeadingSlash(key)})
	if err != nil {
		return xhhCOSUploadCredentialOfficial{}, err
	}
	mimetypes, err := json.Marshal([]string{mimeType})
	if err != nil {
		return xhhCOSUploadCredentialOfficial{}, err
	}
	body := "bucket=" + url.QueryEscape(bucket) +
		"&keys=" + url.QueryEscape(string(keys)) +
		"&mimetypes=" + url.QueryEscape(string(mimetypes)) +
		"&is_multipart_upload=0"
	data, err := postXHHOfficialCOS(ctx, xhhCOSUploadTokenPath, "", body)
	if err != nil {
		return xhhCOSUploadCredentialOfficial{}, err
	}

	var parsed xhhCOSUploadTokenRespOfficial
	if err := json.Unmarshal(data, &parsed); err != nil {
		return xhhCOSUploadCredentialOfficial{}, err
	}
	if parsed.Status != "ok" {
		return xhhCOSUploadCredentialOfficial{}, xhhCOSStatusErrorOfficial("XHH COS upload token", parsed.Status, parsed.Msg)
	}
	credential := xhhCOSUploadCredentialOfficial{
		TmpSecretID:  parsed.Result.Credentials.TmpSecretID,
		TmpSecretKey: parsed.Result.Credentials.TmpSecretKey,
		SessionToken: parsed.Result.Credentials.SessionToken,
		StartTime:    parsed.Result.StartTime,
		ExpiredTime:  parsed.Result.ExpiredTime,
	}
	if credential.TmpSecretID == "" || credential.TmpSecretKey == "" || credential.SessionToken == "" || credential.StartTime == 0 || credential.ExpiredTime == 0 {
		return xhhCOSUploadCredentialOfficial{}, errors.New("XHH COS upload token response missing STS credential")
	}
	return credential, nil
}

func putXHHCOSObjectOfficial(ctx context.Context, uploadURL, bucket, key string, imageBytes []byte, mimeType string, credential xhhCOSUploadCredentialOfficial) error {
	key = ensureLeadingSlash(key)
	query := map[string]string{
		"bucket":   bucket,
		"keys":     mustJSONStringsOfficial([]string{key}),
		"mimetype": mimeType,
	}
	headers := map[string]string{
		"content-type":         mimeType,
		"host":                 strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(uploadURL, "https://"), "http://"), key),
		"x-cos-security-token": credential.SessionToken,
	}
	queryString := sortedCOSPairsOfficial(query)
	headers["host"] = xhhCOSUploadHostOfficial(uploadURL)
	authorization := buildXHHCOSAuthorizationOfficial("put", key, query, headers, credential)
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
		return fmt.Errorf("XHH COS official PUT failed: status=%d body=%s", resp.StatusCode, limitCOSString(string(data), 300))
	}
	return nil
}

func callbackXHHCOSUploadOfficial(ctx context.Context, key string) (string, error) {
	keys, err := json.Marshal([]string{ensureLeadingSlash(key)})
	if err != nil {
		return "", err
	}
	data, err := postXHHOfficialCOS(ctx, xhhCOSUploadCallbackPathOfficial, "&is_finished=true", "keys="+url.QueryEscape(string(keys)))
	if err != nil {
		return "", err
	}
	var parsed xhhCOSUploadCallbackRespOfficial
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	if parsed.Status != "ok" {
		return "", xhhCOSStatusErrorOfficial("XHH COS upload callback", parsed.Status, parsed.Msg)
	}
	if len(parsed.Result.PreviewURLs) > 0 {
		return parsed.Result.PreviewURLs[0], nil
	}
	if len(parsed.Result.Thumbs) > 0 {
		return parsed.Result.Thumbs[0], nil
	}
	return "", nil
}

func postXHHOfficialCOS(ctx context.Context, path, extraQuery, body string) ([]byte, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.ConfigStruct.Xhh.BaseUrl), "/")
	if baseURL == "" {
		baseURL = "https://api.xiaoheihe.cn"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path+"?"+xhhCOSOfficialWebQuery+extraQuery, strings.NewReader(body))
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
		return nil, fmt.Errorf("XHH COS official API failed: path=%s status=%d body=%s", path, resp.StatusCode, limitCOSString(string(data), 300))
	}
	return data, nil
}

func buildXHHCOSAuthorizationOfficial(method, key string, query, headers map[string]string, credential xhhCOSUploadCredentialOfficial) string {
	keyTime := fmt.Sprintf("%d;%d", credential.StartTime, credential.ExpiredTime)
	formatString := strings.ToLower(method) + "\n" + ensureLeadingSlash(key) + "\n" + sortedCOSPairsOfficial(query) + "\n" + sortedCOSPairsOfficial(headers) + "\n"
	stringToSign := "sha1\n" + keyTime + "\n" + sha1HexOfficial(formatString) + "\n"
	signKey := hmacSHA1HexOfficial(credential.TmpSecretKey, keyTime)
	signature := hmacSHA1HexOfficial(signKey, stringToSign)
	return "q-sign-algorithm=sha1" +
		"&q-ak=" + credential.TmpSecretID +
		"&q-sign-time=" + keyTime +
		"&q-key-time=" + keyTime +
		"&q-header-list=" + strings.Join(sortedKeysOfficial(headers), ";") +
		"&q-url-param-list=" + strings.Join(sortedKeysOfficial(query), ";") +
		"&q-signature=" + signature
}

func sortedCOSPairsOfficial(values map[string]string) string {
	keys := sortedKeysOfficial(values)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, cosEscapeOfficial(key)+"="+cosEscapeOfficial(values[key]))
	}
	return strings.Join(pairs, "&")
}

func sortedKeysOfficial(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, strings.ToLower(key))
	}
	sort.Strings(keys)
	return keys
}

func cosEscapeOfficial(value string) string {
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

func sha1HexOfficial(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func hmacSHA1HexOfficial(secret, value string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func xhhCOSUploadFileNameOfficial(sourcePath string, imageBytes []byte) string {
	name := filepath.Base(strings.TrimSpace(sourcePath))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "generated" + imageExtFromBytesOrPath(imageBytes, sourcePath)
	}
	return name
}

func imageDimensionsOfficial(imageBytes []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(imageBytes))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func mustJSONStringsOfficial(values []string) string {
	data, _ := json.Marshal(values)
	return string(data)
}

func xhhCOSUploadHostOfficial(uploadURL string) string {
	parsed, err := url.Parse(uploadURL)
	if err != nil {
		return ""
	}
	return parsed.Host
}

func xhhCOSStatusErrorOfficial(action, status, msg string) error {
	if msg != "" {
		return fmt.Errorf("%s failed: status=%s msg=%s", action, status, msg)
	}
	return fmt.Errorf("%s failed: status=%s", action, status)
}
