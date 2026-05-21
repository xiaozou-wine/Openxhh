package db

import (
	"database/sql"
	"openxhh/config"
	"openxhh/sqlite"
	"testing"
)

func setupSQLiteMessageStreamTest(t *testing.T) {
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
	migrateMessageStreamTables()
	migrateMessageStreamTables()
}

func TestSaveOutboundMessageDedupesByCommentID(t *testing.T) {
	setupSQLiteMessageStreamTest(t)
	first := OutboundMessage{Source: "ai_reply", LinkID: 1, RootCommentID: 2, ReplyCommentID: 3, CommentID: 4, Text: "first", CreatedAt: 10}
	second := first
	second.Text = "second"
	second.CreatedAt = 20
	if !SaveOutboundMessage(first) || !SaveOutboundMessage(second) {
		t.Fatal("SaveOutboundMessage returned false")
	}
	var count int
	var text string
	if err := sqlite.Db.QueryRow("SELECT COUNT(*), MAX(text) FROM outbound_messages WHERE comment_id=?", 4).Scan(&count, &text); err != nil {
		t.Fatalf("query outbound_messages: %v", err)
	}
	if count != 1 || text != "second" {
		t.Fatalf("outbound row = (%d,%q), want (1,second)", count, text)
	}
}

func TestSaveInboundMessageDedupesByMessageID(t *testing.T) {
	setupSQLiteMessageStreamTest(t)
	first := InboundMessage{Source: "at_comment", MessageID: 100, LinkID: 1, CommentID: 2, UserID: 3, UserName: "用户", Text: "first", CreatedAt: 10}
	second := first
	second.Text = "second"
	second.CreatedAt = 20
	if !SaveInboundMessage(first) || !SaveInboundMessage(second) {
		t.Fatal("SaveInboundMessage returned false")
	}
	var count int
	var text string
	if err := sqlite.Db.QueryRow("SELECT COUNT(*), MAX(text) FROM inbound_messages WHERE message_id=?", 100).Scan(&count, &text); err != nil {
		t.Fatalf("query inbound_messages: %v", err)
	}
	var createdAt int64
	if err := sqlite.Db.QueryRow("SELECT created_at FROM inbound_messages WHERE message_id=?", 100).Scan(&createdAt); err != nil {
		t.Fatalf("query inbound created_at: %v", err)
	}
	if count != 1 || text != "second" || createdAt != 10 {
		t.Fatalf("inbound row = (%d,%q,%d), want (1,second,10)", count, text, createdAt)
	}
}

func TestRecentOutboundMessagesAndUpdateComment(t *testing.T) {
	setupSQLiteMessageStreamTest(t)
	record := OutboundMessage{Source: "feed_reply", LinkID: 10, RootCommentID: -1, ReplyCommentID: -1, Text: "hello", CreatedAt: 100}
	if !SaveOutboundMessage(record) {
		t.Fatal("SaveOutboundMessage returned false")
	}
	records := RecentOutboundMessages(90, 10)
	if len(records) != 1 {
		t.Fatalf("len(RecentOutboundMessages) = %d, want 1", len(records))
	}
	if !UpdateOutboundMessageComment(records[0].UniqueKey, 55, 55) {
		t.Fatal("UpdateOutboundMessageComment returned false")
	}
	records = RecentOutboundMessages(90, 10)
	if records[0].CommentID != 55 || records[0].RootCommentID != 55 {
		t.Fatalf("updated ids = (%d,%d), want (55,55)", records[0].CommentID, records[0].RootCommentID)
	}
}

func TestOutboundMessagesForTrackingUsesCursor(t *testing.T) {
	setupSQLiteMessageStreamTest(t)
	records := []OutboundMessage{
		{Source: "ai_reply", LinkID: 1, CommentID: 1, Text: "new-b", CreatedAt: 300, UniqueKey: "b"},
		{Source: "ai_reply", LinkID: 1, CommentID: 2, Text: "new-a", CreatedAt: 300, UniqueKey: "a"},
		{Source: "ai_reply", LinkID: 1, CommentID: 3, Text: "old", CreatedAt: 200, UniqueKey: "c"},
		{Source: "ai_reply", LinkID: 1, CommentID: 4, Text: "too-old", CreatedAt: 100, UniqueKey: "d"},
	}
	for _, record := range records {
		if !SaveOutboundMessage(record) {
			t.Fatalf("SaveOutboundMessage(%q) returned false", record.Text)
		}
	}
	first := OutboundMessagesForTracking(150, 0, "", 2)
	if len(first) != 2 || first[0].UniqueKey != "b" || first[1].UniqueKey != "a" {
		t.Fatalf("first batch = %#v, want b,a", first)
	}
	second := OutboundMessagesForTracking(150, first[1].CreatedAt, first[1].UniqueKey, 2)
	if len(second) != 1 || second[0].UniqueKey != "c" {
		t.Fatalf("second batch = %#v, want c", second)
	}
}
