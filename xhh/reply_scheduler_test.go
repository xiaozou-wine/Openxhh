package xhh

import (
	"database/sql"
	"openxhh/config"
	"openxhh/db"
	"openxhh/sqlite"
	"testing"
)

func resetReplySchedulerState(t *testing.T) {
	t.Helper()
	oldOwner := config.ConfigStruct.Xhh.Owner
	oldOwners := append([]int(nil), Owners...)
	oldOwnerIDsLoaded := ownerIDsLoaded
	oldMaxReplyThreads := MaxReplyThreads
	oldMaxPendingReplies := MaxPendingReplies
	t.Cleanup(func() {
		config.ConfigStruct.Xhh.Owner = oldOwner
		Owners = oldOwners
		ownerIDsLoaded = oldOwnerIDsLoaded
		MaxReplyThreads = oldMaxReplyThreads
		MaxPendingReplies = oldMaxPendingReplies
	})
}

func setupXHHSQLiteCommTest(t *testing.T) {
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

func insertSchedulerCommForTest(t *testing.T, msgID, userID int) {
	t.Helper()
	_, err := sqlite.Db.Exec("INSERT INTO at (msg_id, comment_a_id, comment_root_id, link_id, user_a_id, user_a_name, comment_text, reply) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", msgID, msgID+1000, -1, 1, userID, "user", "text", false)
	if err != nil {
		t.Fatalf("insert comm: %v", err)
	}
}

func TestSelectReplyBatchUsesLimitForNormalUsers(t *testing.T) {
	resetReplySchedulerState(t)
	config.ConfigStruct.Xhh.Owner = "100"
	Owners = nil
	ownerIDsLoaded = false
	MaxReplyThreads = 3

	got := selectReplyBatch([]db.CommStruct{
		{MsgID: 1, Uid: 200},
		{MsgID: 2, Uid: 201},
		{MsgID: 3, Uid: 202},
		{MsgID: 4, Uid: 203},
	}, nil, 3, replyKindNormal)

	if len(got) != 3 {
		t.Fatalf("len(selectReplyBatch) = %d, want 3", len(got))
	}
	if got[0].MsgID != 1 || got[1].MsgID != 2 || got[2].MsgID != 3 {
		t.Fatalf("selected msg ids = [%d %d %d], want [1 2 3]", got[0].MsgID, got[1].MsgID, got[2].MsgID)
	}
}

func TestSelectReplyBatchDoesNotLimitOwners(t *testing.T) {
	resetReplySchedulerState(t)
	config.ConfigStruct.Xhh.Owner = "100"
	Owners = nil
	ownerIDsLoaded = false
	MaxReplyThreads = 2

	got := selectReplyBatch([]db.CommStruct{
		{MsgID: 1, Uid: 200},
		{MsgID: 2, Uid: 100},
		{MsgID: 3, Uid: 100},
		{MsgID: 4, Uid: 100},
	}, nil, 0, replyKindOwner)

	if len(got) != 3 {
		t.Fatalf("len(selectReplyBatch) = %d, want 3", len(got))
	}
	for _, item := range got {
		if item.Uid != 100 {
			t.Fatalf("selected non-owner reply: %+v", item)
		}
	}
}

func TestReplyThreadLimitDefaultsWhenConfigInvalid(t *testing.T) {
	resetReplySchedulerState(t)
	MaxReplyThreads = 0

	if got := replyThreadLimit(); got != defaultMaxReplyThreads {
		t.Fatalf("replyThreadLimit = %d, want %d", got, defaultMaxReplyThreads)
	}
}

func TestNextReplyBatchIncludesOwnersAndNormalSlots(t *testing.T) {
	resetReplySchedulerState(t)
	setupXHHSQLiteCommTest(t)
	config.ConfigStruct.Xhh.Owner = "100"
	Owners = nil
	ownerIDsLoaded = false
	MaxReplyThreads = 2
	insertSchedulerCommForTest(t, 10, 200)
	insertSchedulerCommForTest(t, 20, 201)
	insertSchedulerCommForTest(t, 25, 202)
	insertSchedulerCommForTest(t, 30, 100)
	insertSchedulerCommForTest(t, 40, 100)
	insertSchedulerCommForTest(t, 50, 100)

	got := nextReplyBatch()
	if len(got) != 5 {
		t.Fatalf("len(nextReplyBatch) = %d, want 5", len(got))
	}
	ownerCount := 0
	normalCount := 0
	for _, item := range got {
		if item.Uid == 100 {
			ownerCount++
		} else {
			normalCount++
		}
	}
	if ownerCount != 3 || normalCount != 2 {
		t.Fatalf("nextReplyBatch owner=%d normal=%d, want owner=3 normal=2", ownerCount, normalCount)
	}
}

func TestNextReplyBatchFallsBackToNormalThreadLimit(t *testing.T) {
	resetReplySchedulerState(t)
	setupXHHSQLiteCommTest(t)
	config.ConfigStruct.Xhh.Owner = "100"
	Owners = nil
	ownerIDsLoaded = false
	MaxReplyThreads = 3
	insertSchedulerCommForTest(t, 10, 200)
	insertSchedulerCommForTest(t, 20, 201)
	insertSchedulerCommForTest(t, 30, 202)
	insertSchedulerCommForTest(t, 40, 203)

	got := nextReplyBatch()
	if len(got) != 3 {
		t.Fatalf("len(nextReplyBatch) = %d, want 3", len(got))
	}
	if got[0].MsgID != 10 || got[1].MsgID != 20 || got[2].MsgID != 30 {
		t.Fatalf("nextReplyBatch msg ids = [%d %d %d], want [10 20 30]", got[0].MsgID, got[1].MsgID, got[2].MsgID)
	}
}

func TestNextOwnerReplyBatchSkipsInFlightReply(t *testing.T) {
	resetReplySchedulerState(t)
	setupXHHSQLiteCommTest(t)
	config.ConfigStruct.Xhh.Owner = "100"
	Owners = nil
	ownerIDsLoaded = false
	MaxReplyThreads = 1
	insertSchedulerCommForTest(t, 10, 100)
	insertSchedulerCommForTest(t, 20, 100)
	insertSchedulerCommForTest(t, 30, 100)

	got := nextOwnerReplyBatch(map[int]string{10: replyKindOwner})
	if len(got) != 2 {
		t.Fatalf("len(nextOwnerReplyBatch) = %d, want 2", len(got))
	}
	if got[0].MsgID != 20 || got[1].MsgID != 30 {
		t.Fatalf("nextOwnerReplyBatch msg ids = [%d %d], want [20 30]", got[0].MsgID, got[1].MsgID)
	}
}

func TestNextNormalReplyBatchIgnoresActiveOwner(t *testing.T) {
	resetReplySchedulerState(t)
	setupXHHSQLiteCommTest(t)
	config.ConfigStruct.Xhh.Owner = "100"
	Owners = nil
	ownerIDsLoaded = false
	MaxReplyThreads = 2
	insertSchedulerCommForTest(t, 10, 200)
	insertSchedulerCommForTest(t, 20, 201)
	insertSchedulerCommForTest(t, 30, 202)

	got := nextNormalReplyBatch(map[int]string{40: replyKindOwner})
	if len(got) != 2 {
		t.Fatalf("len(nextNormalReplyBatch) = %d, want 2", len(got))
	}
	if got[0].MsgID != 10 || got[1].MsgID != 20 {
		t.Fatalf("nextNormalReplyBatch msg ids = [%d %d], want [10 20]", got[0].MsgID, got[1].MsgID)
	}
}

func TestNextNormalReplyBatchSkipsInFlightNormalReplies(t *testing.T) {
	resetReplySchedulerState(t)
	setupXHHSQLiteCommTest(t)
	config.ConfigStruct.Xhh.Owner = "100"
	Owners = nil
	ownerIDsLoaded = false
	MaxReplyThreads = 3
	insertSchedulerCommForTest(t, 10, 200)
	insertSchedulerCommForTest(t, 20, 201)
	insertSchedulerCommForTest(t, 30, 202)
	insertSchedulerCommForTest(t, 40, 203)

	got := nextNormalReplyBatch(map[int]string{10: replyKindNormal, 20: replyKindNormal})
	if len(got) != 1 {
		t.Fatalf("len(nextNormalReplyBatch) = %d, want 1", len(got))
	}
	if got[0].MsgID != 30 {
		t.Fatalf("nextNormalReplyBatch selected MsgID %d, want 30", got[0].MsgID)
	}
}

func TestOwnerIDsCachesEmptyResult(t *testing.T) {
	resetReplySchedulerState(t)
	config.ConfigStruct.Xhh.Owner = "bad"
	Owners = nil
	ownerIDsLoaded = false

	if got := ownerIDs(); len(got) != 0 {
		t.Fatalf("ownerIDs = %v, want empty", got)
	}
	config.ConfigStruct.Xhh.Owner = "100"
	if got := ownerIDs(); len(got) != 0 {
		t.Fatalf("ownerIDs after cached invalid config = %v, want empty", got)
	}
}
