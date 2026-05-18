package main

import (
	"bufio"
	"flag"
	"fmt"
	"openxhh/config"
	"openxhh/db"
	"openxhh/loger"
	"openxhh/xhh"
	"os"
	"strings"
	"time"
)

func main() {
	loger.InitLog()
	mode := flag.String("mode", "default", "Switch a mode when start")
	flag.Parse()
	config.InitConfig()
	time.Sleep(1 * time.Second)
	if *mode == "start" {
		db.Init()
	}
	start(mode)
}

func CheckNew() {
	if !db.IsNew() {
		return
	}
	fmt.Println("检测到您是第一次运行\n是否允许将先前@过的名单加入至艾特列表？\ny(es) or n(o) 默认n\n请输入y或n")
	input := bufio.NewReader(os.Stdin)
	str, err := input.ReadString('\n')
	if err != nil {
		loger.Loger.Fatal("[MAIN]无法读取您的输入")
	}
	in := strings.TrimRight(str, "\r\n")

	switch in {
	case "n":
		xhh.DontReply = true
		return
	case "no":
		xhh.DontReply = true
		return
	case "N":
		xhh.DontReply = true
		return
	case "No":
		xhh.DontReply = true
		return
	case "NO":
		xhh.DontReply = true
		return
	default:
		xhh.DontReply = true
		return
	}
}

func start(mode *string) {
	switch *mode {
	case "default":
		loger.Loger.Info("\nhttps://github.com/Www8881313/Openxhh\n你需要输入启动项\n-mode start | login | test")
	case "test":
		fmt.Println("你好")
	case "login":
		xhh.Login()
	case "start":
		CheckNew()
		xhh.Init()
		xhh.Start()
		select {}
	}

}
