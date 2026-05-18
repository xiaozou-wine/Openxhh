package xhh

import (
	"openxhh/config"
	"openxhh/loger"
	"strconv"
	"strings"
)

var Owners []int

func Check(UID int) bool {
	cfg := config.ConfigStruct.Xhh
	if !cfg.EnableWhitelist {
		return true
	}
	if strings.TrimSpace(cfg.Owner) == "" {
		loger.Loger.Fatal("您已启用白名单，但未在配置中设置 xhh.owner")
		return false
	}
	return IsOwner(UID)
}

func IsOwner(UID int) bool {
	for _, v := range ownerIDs() {
		if v == UID {
			return true
		}
	}
	return false
}

func ownerIDs() []int {
	if len(Owners) > 0 {
		return Owners
	}
	OwnArr := strings.Split(config.ConfigStruct.Xhh.Owner, ",")
	for _, v := range OwnArr {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		i, err := strconv.Atoi(v)
		if err != nil {
			loger.Loger.Error("[XHH]您的 owner 配置->" + v + "<-似乎并非数字")
			continue
		}
		Owners = append(Owners, i)
	}
	return Owners
}
