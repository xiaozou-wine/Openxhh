package config

import (
	"encoding/json"
	"os"
	"xhhrobot/loger"
)

var ConfigStruct struct {
	Xhh struct {
		BaseUrl string `json:"baseUrl"`
		WebVer  string `json:"webver"`
		Ver     string `json:"version"`
	} `json:"xhh"`
	DataBase struct {
		Db     string `json:"db"`
		Host   string `json:"host"`
		Port   string `json:"port"`
		User   string `json:"user"`
		Passwd string `json:"passwd"`
	} `json:"database"`
}

func InitConfig() {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	file, err := os.ReadFile(wd + "/config.json")
	if err != nil {
		if os.IsNotExist(err) {
			Data, err := json.Marshal(ConfigStruct)
			if err != nil {
				panic(err)
			}
			os.WriteFile("./config.json", Data, 0644)
			loger.Loger.Fatal("Plz edit config and restart")
		}
		panic(err)
	}
	err = json.Unmarshal(file, &ConfigStruct)
	if err != nil {
		panic(err)
	}
}
