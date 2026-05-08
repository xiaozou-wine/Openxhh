package main

import (
	"flag"
	"fmt"
	"xhhrobot/config"
	"xhhrobot/loger"
	"xhhrobot/pg"
	"xhhrobot/xhh"
)

func main() {
	loger.InitLog()
	config.InitConfig()
	pg.InitPostgreSQL()
	xhh.Init()
	mode := flag.String("mode", "default", "Switch a mode when start")
	flag.Parse()
	start(mode)
}

func start(mode *string) {
	switch *mode {
	case "default":
		loger.Loger.Info("\nHi,This is XhhRobot\nYour Should Set a Mode\n--mode start | login | test")
	case "test":
		fmt.Println("TEST")
	case "login":
		xhh.Login()
	case "start":
		xhh.Start()
		select {}
	}

}
