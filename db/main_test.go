package db

import (
	"database/sql"
	"openxhh/config"
	"openxhh/sqlite"
	"testing"
)

func setupSQLiteCommTest(t *testing.T) {
	t.Helper()
	oldType := config.ConfigStruct.DataBase.Type
	oldDB := sqlite.Db
	database, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlite.Db = database
	config.ConfigStruct.DataBase.Type = "sqlite"
	t.Cleanup(func() {
		database.Close()
		sqlite.Db = oldDB
		config.ConfigStruct.DataBase.Type = oldType
	})
	_, err = sqlite.Db.Exec(`CREATE TABLE at (
		msg_id BIGINT PRIMARY KEY,
		comment_a_id BIGINT,
		comment_root_id BIGINT,
		link_id BIGINT,
		user_a_id BIGINT,
		user_a_name TEXT DEFAULT '',
		comment_text TEXT,
		reply boolean
	)`)
	if err != nil {
		t.Fatalf("create at table: %v", err)
	}
}

func insertCommForTest(t *testing.T, msgID, userID int, replied bool) {
	t.Helper()
	_, err := sqlite.Db.Exec("INSERT INTO at (msg_id, comment_a_id, comment_root_id, link_id, user_a_id, user_a_name, comment_text, reply) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", msgID, msgID+1000, -1, 1, userID, "user", "text", replied)
	if err != nil {
		t.Fatalf("insert comm: %v", err)
	}
}

func TestGetCommOrdersPendingByMessageID(t *testing.T) {
	setupSQLiteCommTest(t)
	insertCommForTest(t, 30, 200, false)
	insertCommForTest(t, 10, 201, false)
	insertCommForTest(t, 20, 202, true)

	got := GetComm(2)
	if len(got) != 2 {
		t.Fatalf("len(GetComm) = %d, want 2", len(got))
	}
	if got[0].MsgID != 10 || got[1].MsgID != 30 {
		t.Fatalf("GetComm msg ids = [%d %d], want [10 30]", got[0].MsgID, got[1].MsgID)
	}
}

func TestGetCommByUserIDsReturnsOnlyOwners(t *testing.T) {
	setupSQLiteCommTest(t)
	insertCommForTest(t, 10, 200, false)
	insertCommForTest(t, 20, 100, false)
	insertCommForTest(t, 30, 100, false)
	insertCommForTest(t, 40, 100, false)

	got := GetCommByUserIDs([]int{100}, 2)
	if len(got) != 2 {
		t.Fatalf("len(GetCommByUserIDs) = %d, want 2", len(got))
	}
	if got[0].Uid != 100 || got[1].Uid != 100 {
		t.Fatalf("GetCommByUserIDs returned non-owner rows: %+v", got)
	}
	if got[0].MsgID != 20 || got[1].MsgID != 30 {
		t.Fatalf("GetCommByUserIDs msg ids = [%d %d], want [20 30]", got[0].MsgID, got[1].MsgID)
	}
}

func TestGetCommExcludingUserIDsSkipsOwners(t *testing.T) {
	setupSQLiteCommTest(t)
	insertCommForTest(t, 10, 100, false)
	insertCommForTest(t, 20, 200, false)
	insertCommForTest(t, 30, 201, false)

	got := GetCommExcludingUserIDs([]int{100}, 1)
	if len(got) != 1 {
		t.Fatalf("len(GetCommExcludingUserIDs) = %d, want 1", len(got))
	}
	if got[0].Uid == 100 || got[0].MsgID != 20 {
		t.Fatalf("GetCommExcludingUserIDs returned %+v, want msg 20 non-owner", got[0])
	}
}

func TestGetCommByUserIDsWithoutLimitReturnsAllOwners(t *testing.T) {
	setupSQLiteCommTest(t)
	insertCommForTest(t, 10, 100, false)
	insertCommForTest(t, 20, 100, false)
	insertCommForTest(t, 30, 100, false)
	insertCommForTest(t, 40, 200, false)

	got := GetCommByUserIDs([]int{100}, 0)
	if len(got) != 3 {
		t.Fatalf("len(GetCommByUserIDs) = %d, want 3", len(got))
	}
	if got[0].MsgID != 10 || got[1].MsgID != 20 || got[2].MsgID != 30 {
		t.Fatalf("GetCommByUserIDs msg ids = [%d %d %d], want [10 20 30]", got[0].MsgID, got[1].MsgID, got[2].MsgID)
	}
}
