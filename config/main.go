package config

import (
	"encoding/json"
	"openxhh/loger"
	"os"
)

var ConfigStruct struct {
	Xhh struct {
		CheckTime int    `json:"checkTime"`
		ReplyTime int    `json:"replyTime"`
		Owner     string `json:"owner"`
		DeviceID  string `json:"deviceID"`
		BaseUrl   string `json:"baseUrl"`
		WebVer    string `json:"webver"`
		Ver       string `json:"version"`
	} `json:"xhh"`
	DataBase struct {
		Type   string `json:"type"`
		Db     string `json:"db"`
		Host   string `json:"host"`
		Port   string `json:"port"`
		User   string `json:"user"`
		Passwd string `json:"passwd"`
	} `json:"database"`
	Ai struct {
		Model   string `json:"model"`
		Prompt  string `json:"prompt"`
		BaseUrl string `json:"baseUrl"`
		Token   string `json:"token"`
	} `json:"ai"`
	Image struct {
		Model           string `json:"model"`
		BaseUrl         string `json:"baseUrl"`
		Token           string `json:"token"`
		Size            string `json:"size"`
		ResponseFormat  string `json:"responseFormat"`
		OutputDir       string `json:"outputDir"`
		UploadMode      string `json:"uploadMode"`
		ExternalDir     string `json:"externalDir"`
		ExternalBaseUrl string `json:"externalBaseUrl"`
		PromptRefine    bool   `json:"promptRefine"`
		PromptModel     string `json:"promptModel"`
		PromptBaseUrl   string `json:"promptBaseUrl"`
		PromptToken     string `json:"promptToken"`
		PromptMaxChars  int    `json:"promptMaxChars"`
	} `json:"image"`
}

func InitConfig() {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	file, err := os.ReadFile(wd + "/config.json")
	if err != nil {
		if os.IsNotExist(err) {
			applyDefaults()
			Data, err := json.MarshalIndent(ConfigStruct, "", "  ")
			if err != nil {
				panic(err)
			}
			os.WriteFile("./config.json", append(Data, '\n'), 0644)
			loger.Loger.Fatal("请修改配置文件后重新启动")
		}
		panic(err)
	}
	err = json.Unmarshal(file, &ConfigStruct)
	if err != nil {
		panic(err)
	}
	if applyDefaults() {
		if Data, err := json.MarshalIndent(ConfigStruct, "", "  "); err == nil {
			_ = os.WriteFile("./config.json", append(Data, '\n'), 0644)
		}
	}
	loger.Loger.Info("[CFG]Init OK")
}

func applyDefaults() bool {
	changed := false
	if ConfigStruct.Xhh.CheckTime == 0 {
		ConfigStruct.Xhh.CheckTime = 60
		changed = true
	}
	if ConfigStruct.Xhh.ReplyTime == 0 {
		ConfigStruct.Xhh.ReplyTime = 30
		changed = true
	}
	if ConfigStruct.Xhh.BaseUrl == "" {
		ConfigStruct.Xhh.BaseUrl = "https://api.xiaoheihe.cn"
		changed = true
	}
	if ConfigStruct.Xhh.WebVer == "" {
		ConfigStruct.Xhh.WebVer = "2.5"
		changed = true
	}
	if ConfigStruct.Xhh.Ver == "" {
		ConfigStruct.Xhh.Ver = "999.0.4"
		changed = true
	}
	if ConfigStruct.DataBase.Type == "" {
		ConfigStruct.DataBase.Type = "sqlite"
		changed = true
	}
	if ConfigStruct.Image.Model == "" {
		ConfigStruct.Image.Model = "gpt-image-2"
		changed = true
	}
	if ConfigStruct.Image.Size == "" {
		ConfigStruct.Image.Size = "1024x1024"
		changed = true
	}
	if ConfigStruct.Image.ResponseFormat == "" {
		ConfigStruct.Image.ResponseFormat = "b64_json"
		changed = true
	}
	if ConfigStruct.Image.OutputDir == "" {
		ConfigStruct.Image.OutputDir = "images"
		changed = true
	}
	if ConfigStruct.Image.UploadMode == "" {
		ConfigStruct.Image.UploadMode = "external"
		changed = true
	}
	if ConfigStruct.Image.PromptMaxChars == 0 {
		ConfigStruct.Image.PromptMaxChars = 1000
		changed = true
	}
	return changed
}
