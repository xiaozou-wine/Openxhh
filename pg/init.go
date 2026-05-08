package pg

import (
	"context"
	"fmt"
	"xhhrobot/config"
	"xhhrobot/loger"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

var Conn *pgx.Conn

func InitPostgreSQL() {
	UserName := config.ConfigStruct.DataBase.User
	Passwd := config.ConfigStruct.DataBase.Passwd
	Host := config.ConfigStruct.DataBase.Host
	Port := config.ConfigStruct.DataBase.Port
	Db := config.ConfigStruct.DataBase.Db
	ConnStr := fmt.Sprintf("postgresql://%s:%s@%s:%s/%s", UserName, Passwd, Host, Port, Db)
	var err error
	Conn, err = pgx.Connect(context.Background(), ConnStr)
	if err != nil {
		loger.Loger.Fatal("[DB]Failed to Connect Database", zap.Error(err))
	}
	err = Conn.Ping(context.Background())
	if err != nil {
		loger.Loger.Fatal("[DB]Fatal Error.", zap.Error(err))
	}
	loger.Loger.Info("[DB]PgSQL is OK!")
}
