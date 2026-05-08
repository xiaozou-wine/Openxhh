package xhh

import "time"

func Start() {
	go func() {
		for {
			AutoReply()
			time.Sleep(5 * time.Second)
		}
	}()
	go func() {
		for {
			CheckAt()
			time.Sleep(10 * time.Second)
		}
	}()
}
