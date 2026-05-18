package xhh

import (
	"crypto/md5"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/url"
	"openxhh/loger"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
	"go.uber.org/zap"
)

func Login() {
	Qr()
}

type data struct {
	Status  string `json:"status"`
	Msg     string `json:"msg"`
	Version string `json:"version"`
	Result  struct {
		Qrcode   string `json:"qr_url"`
		Expire   int    `json:"expire"`
		ErrMsg   string `json:"error_msg"`
		Err      string `json:"error"`
		NickName string `json:"nickname"`
	} `json:"result"`
}

func Qr() {
	fmt.Println("扫码登陆")
	Path := "/account/get_qrcode_url/"
	resp := SendReq("GET", Path, nil, "")
	if resp == nil {
		loger.Loger.Error("[XHH]无法创建请求")
		return
	}
	var resps data
	read, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	fmt.Println(string(read))
	if err != nil {
		loger.Loger.Error("[XHH]Can't Read Body")
		return
	}
	err = json.Unmarshal(read, &resps)
	if err != nil {
		loger.Loger.Error("[XHH]Can't unmarshal body")
		return
	}
	qrURL := strings.TrimSpace(resps.Result.Qrcode)
	if qrURL == "" {
		loger.Loger.Error("[XHH]登录二维码为空")
		return
	}
	qrLoginURL, err := url.Parse(qrURL)
	if err != nil || qrLoginURL.RawQuery == "" {
		loger.Loger.Error("[XHH]登录二维码地址格式异常", zap.String("qr_url", qrURL), zap.Error(err))
		return
	}
	qrStateQuery := "?" + qrLoginURL.RawQuery
	code, err := qrcode.New(qrURL, qrcode.Low)
	if err != nil {
		loger.Loger.Error("[XHH]无法生成二维码", zap.Error(err))
		return
	}
	err = code.WriteFile(256, "qrcode.png")
	if err != nil {
		loger.Loger.Error("[XHH]创建二维码图片失败", zap.Error(err))
		return
	}
	fmt.Println("二维码图片已保存到 qrcode.png")
	ascii := code.ToSmallString(true)
	fmt.Println(ascii)
	for {
		path := "/account/qr_state/"
		resp := SendReq("GET", path, nil, qrStateQuery)
		if resp == nil {
			loger.Loger.Error("[XHH]无法查询扫码状态")
			return
		}
		var resps data
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			_ = resp.Body.Close()
			loger.Loger.Error("[XHH]无法读取body")
			return
		}
		err = json.Unmarshal(data, &resps)
		if err != nil {
			_ = resp.Body.Close()
			loger.Loger.Error("[XHH]无法反序列化")
			return
		}
		fmt.Printf("\r %v | %v | %v", resps.Result.Err, resps.Result.ErrMsg, resps)
		if resps.Result.Err != "ok" {
			_ = resp.Body.Close()
			time.Sleep(1 * time.Second)
			continue
		}
		cookie := resp.Cookies()
		_ = resp.Body.Close()
		if len(cookie) < 2 {
			loger.Loger.Error("[XHH]扫码成功但未返回完整 Cookie")
			return
		}
		Info.Cookie = cookie[0].Name + "=" + cookie[0].Value + ";" + cookie[1].Name + "=" + cookie[1].Value
		Info.Cookie += GetFuckingToken()
		for _, v := range cookie {
			if v.Name == "user_heybox_id" {
				Info.HeyBoxId = v.Value
			}
		}
		Info.Time = int(time.Now().Unix())
		Jdata, err := json.Marshal(Info)
		if err != nil {
			loger.Loger.Error("[XHH]无法序列化", zap.Error(err))
			return
		}
		err = os.WriteFile("./cookie.json", Jdata, 0775)
		if err != nil {
			loger.Loger.Error("[XHH]创建Cookie失败", zap.Error(err))
			return
		}
		fmt.Printf("\n欢迎您 -> %s | Cookie已保存\n", resps.Result.NickName)
		return
	}
}

func GetFuckingToken() string {
	var rawstr []byte
	_str := strconv.Itoa(int(time.Now().Unix()))
	_md5 := md5.Sum([]byte(_str))
	rawstr = append(rawstr, _md5[:]...)
	_md5 = md5.Sum([]byte("唉？！云朵！"))
	rawstr = append(rawstr, _md5[:]...)
	_md5 = md5.Sum([]byte("哒哒哒哒哒，好想玩原神"))
	rawstr = append(rawstr, _md5[:]...)
	_md5 = md5.Sum([]byte("云！原！神！"))
	rawstr = append(rawstr, _md5[:]...)
	rawstr = append(rawstr, 0)
	str := ";x_xhh_tokenid=" + base64.StdEncoding.EncodeToString([]byte(rawstr))
	return str

}
func RSA(before string) (after string) {
	publicKey := "-----BEGIN PUBLIC KEY-----\nMIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDZgjVwAiKTjZ55nG+mW6r3TSU4\nECvNYqDMIS/bhCj2QaH5GI/KZb2TBp+CBvUj9SLFnmJQ0kzHzHoGZCQ88VevCffF7JePGF9cmKQqotlfTKbV4oxV5iLz7JSG6b/Vg7AXtrTolNtWsa8HiB0tI0YClYaQlOXm4UxLeSxQwSFETwIDAQAB\n-----END PUBLIC KEY-----\n"
	block, _ := pem.Decode([]byte(publicKey))
	if block == nil {
		loger.Loger.Error("[XHH]无法解析公钥")
		return
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		loger.Loger.Error("[XHH]无法解析为标准RSA Key")
		return
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		loger.Loger.Error("[XHH]Key似乎并非RsaKEY")
		return
	}
	c, err := rsa.EncryptPKCS1v15(nil, rsaPub, []byte(before))
	if err != nil {
		loger.Loger.Error("[XHH]加密失败", zap.Error(err))
		return
	}
	After := base64.StdEncoding.EncodeToString(c)
	return After
}
