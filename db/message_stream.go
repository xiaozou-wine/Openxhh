package db

import (
	"context"
	"fmt"
	"strings"

	"openxhh/loger"
	"openxhh/pg"
	"openxhh/sqlite"

	"go.uber.org/zap"
)

type OutboundMessage struct {
	Source         string `json:"source"`
	LinkID         int64  `json:"linkId"`
	RootCommentID  int64  `json:"rootCommentId"`
	ReplyCommentID int64  `json:"replyCommentId"`
	CommentID      int64  `json:"commentId"`
	Text           string `json:"text"`
	ImageURL       string `json:"imageUrl"`
	CreatedAt      int64  `json:"createdAt"`
	RawResponse    string `json:"rawResponse"`
	UniqueKey      string `json:"uniqueKey"`
}

type InboundMessage struct {
	Source         string `json:"source"`
	MessageID      int64  `json:"messageId"`
	LinkID         int64  `json:"linkId"`
	RootCommentID  int64  `json:"rootCommentId"`
	ReplyCommentID int64  `json:"replyCommentId"`
	CommentID      int64  `json:"commentId"`
	UserID         int64  `json:"userId"`
	UserName       string `json:"userName"`
	Text           string `json:"text"`
	CreatedAt      int64  `json:"createdAt"`
	RawResponse    string `json:"rawResponse"`
	UniqueKey      string `json:"uniqueKey"`
}

func migrateMessageStreamTables() {
	ctx := context.Background()
	if cfg.Type == "pg" {
		_, err := pg.Conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS outbound_messages (
			id BIGSERIAL PRIMARY KEY,
			source TEXT DEFAULT '',
			link_id BIGINT DEFAULT 0,
			root_comment_id BIGINT DEFAULT 0,
			reply_comment_id BIGINT DEFAULT 0,
			comment_id BIGINT DEFAULT 0,
			text TEXT DEFAULT '',
			image_url TEXT DEFAULT '',
			created_at BIGINT DEFAULT 0,
			raw_response TEXT DEFAULT '',
			unique_key TEXT UNIQUE
		)`)
		if err != nil {
			loger.Loger.Warn("[DB]无法创建发出消息表", zap.Error(err))
		}
		_, err = pg.Conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS inbound_messages (
			id BIGSERIAL PRIMARY KEY,
			source TEXT DEFAULT '',
			message_id BIGINT DEFAULT 0,
			link_id BIGINT DEFAULT 0,
			root_comment_id BIGINT DEFAULT 0,
			reply_comment_id BIGINT DEFAULT 0,
			comment_id BIGINT DEFAULT 0,
			user_id BIGINT DEFAULT 0,
			user_name TEXT DEFAULT '',
			text TEXT DEFAULT '',
			created_at BIGINT DEFAULT 0,
			raw_response TEXT DEFAULT '',
			unique_key TEXT UNIQUE
		)`)
		if err != nil {
			loger.Loger.Warn("[DB]无法创建收到消息表", zap.Error(err))
		}
	}
	if cfg.Type == "sqlite" {
		_, err := sqlite.Db.Exec(`CREATE TABLE IF NOT EXISTS outbound_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source TEXT DEFAULT '',
			link_id BIGINT DEFAULT 0,
			root_comment_id BIGINT DEFAULT 0,
			reply_comment_id BIGINT DEFAULT 0,
			comment_id BIGINT DEFAULT 0,
			text TEXT DEFAULT '',
			image_url TEXT DEFAULT '',
			created_at BIGINT DEFAULT 0,
			raw_response TEXT DEFAULT '',
			unique_key TEXT UNIQUE
		)`)
		if err != nil {
			loger.Loger.Warn("[DB]无法创建发出消息表", zap.Error(err))
		}
		_, err = sqlite.Db.Exec(`CREATE TABLE IF NOT EXISTS inbound_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source TEXT DEFAULT '',
			message_id BIGINT DEFAULT 0,
			link_id BIGINT DEFAULT 0,
			root_comment_id BIGINT DEFAULT 0,
			reply_comment_id BIGINT DEFAULT 0,
			comment_id BIGINT DEFAULT 0,
			user_id BIGINT DEFAULT 0,
			user_name TEXT DEFAULT '',
			text TEXT DEFAULT '',
			created_at BIGINT DEFAULT 0,
			raw_response TEXT DEFAULT '',
			unique_key TEXT UNIQUE
		)`)
		if err != nil {
			loger.Loger.Warn("[DB]无法创建收到消息表", zap.Error(err))
		}
	}
}

func SaveOutboundMessage(record OutboundMessage) bool {
	if !messageStreamDatabaseReady() {
		return false
	}
	record.Source = strings.TrimSpace(record.Source)
	record.Text = strings.TrimSpace(record.Text)
	record.ImageURL = strings.TrimSpace(record.ImageURL)
	record.RawResponse = strings.TrimSpace(record.RawResponse)
	record.UniqueKey = outboundUniqueKey(record)
	ctx := context.Background()
	if cfg.Type == "pg" {
		_, err := pg.Conn.Exec(ctx, `INSERT INTO outbound_messages (source,link_id,root_comment_id,reply_comment_id,comment_id,text,image_url,created_at,raw_response,unique_key)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (unique_key) DO UPDATE SET source=$1, link_id=$2, root_comment_id=$3, reply_comment_id=$4, comment_id=$5, text=$6, image_url=$7, created_at=$8, raw_response=$9`,
			record.Source, record.LinkID, record.RootCommentID, record.ReplyCommentID, record.CommentID, record.Text, record.ImageURL, record.CreatedAt, record.RawResponse, record.UniqueKey)
		if err != nil {
			loger.Loger.Warn("[DB]无法保存发出消息", zap.Error(err), zap.String("source", record.Source), zap.Int64("link_id", record.LinkID))
			return false
		}
		return true
	}
	if cfg.Type == "sqlite" {
		_, err := sqlite.Db.Exec(`INSERT INTO outbound_messages (source,link_id,root_comment_id,reply_comment_id,comment_id,text,image_url,created_at,raw_response,unique_key)
			VALUES (?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT (unique_key) DO UPDATE SET source=excluded.source, link_id=excluded.link_id, root_comment_id=excluded.root_comment_id, reply_comment_id=excluded.reply_comment_id, comment_id=excluded.comment_id, text=excluded.text, image_url=excluded.image_url, created_at=excluded.created_at, raw_response=excluded.raw_response`,
			record.Source, record.LinkID, record.RootCommentID, record.ReplyCommentID, record.CommentID, record.Text, record.ImageURL, record.CreatedAt, record.RawResponse, record.UniqueKey)
		if err != nil {
			loger.Loger.Warn("[DB]无法保存发出消息", zap.Error(err), zap.String("source", record.Source), zap.Int64("link_id", record.LinkID))
			return false
		}
		return true
	}
	return false
}

func SaveInboundMessage(record InboundMessage) bool {
	if !messageStreamDatabaseReady() {
		return false
	}
	record.Source = strings.TrimSpace(record.Source)
	record.UserName = strings.TrimSpace(record.UserName)
	record.Text = strings.TrimSpace(record.Text)
	record.RawResponse = strings.TrimSpace(record.RawResponse)
	record.UniqueKey = inboundUniqueKey(record)
	ctx := context.Background()
	if cfg.Type == "pg" {
		_, err := pg.Conn.Exec(ctx, `INSERT INTO inbound_messages (source,message_id,link_id,root_comment_id,reply_comment_id,comment_id,user_id,user_name,text,created_at,raw_response,unique_key)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (unique_key) DO UPDATE SET source=$1, message_id=$2, link_id=$3, root_comment_id=$4, reply_comment_id=$5, comment_id=$6, user_id=$7, user_name=$8, text=$9, created_at=CASE WHEN inbound_messages.created_at > 0 THEN inbound_messages.created_at ELSE EXCLUDED.created_at END, raw_response=$11`,
			record.Source, record.MessageID, record.LinkID, record.RootCommentID, record.ReplyCommentID, record.CommentID, record.UserID, record.UserName, record.Text, record.CreatedAt, record.RawResponse, record.UniqueKey)
		if err != nil {
			loger.Loger.Warn("[DB]无法保存收到消息", zap.Error(err), zap.String("source", record.Source), zap.Int64("comment_id", record.CommentID))
			return false
		}
		return true
	}
	if cfg.Type == "sqlite" {
		_, err := sqlite.Db.Exec(`INSERT INTO inbound_messages (source,message_id,link_id,root_comment_id,reply_comment_id,comment_id,user_id,user_name,text,created_at,raw_response,unique_key)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT (unique_key) DO UPDATE SET source=excluded.source, message_id=excluded.message_id, link_id=excluded.link_id, root_comment_id=excluded.root_comment_id, reply_comment_id=excluded.reply_comment_id, comment_id=excluded.comment_id, user_id=excluded.user_id, user_name=excluded.user_name, text=excluded.text, created_at=CASE WHEN inbound_messages.created_at > 0 THEN inbound_messages.created_at ELSE excluded.created_at END, raw_response=excluded.raw_response`,
			record.Source, record.MessageID, record.LinkID, record.RootCommentID, record.ReplyCommentID, record.CommentID, record.UserID, record.UserName, record.Text, record.CreatedAt, record.RawResponse, record.UniqueKey)
		if err != nil {
			loger.Loger.Warn("[DB]无法保存收到消息", zap.Error(err), zap.String("source", record.Source), zap.Int64("comment_id", record.CommentID))
			return false
		}
		return true
	}
	return false
}

func RecentOutboundMessages(since int64, limit int) []OutboundMessage {
	return OutboundMessagesForTracking(since, 0, "", limit)
}

func OutboundMessagesForTracking(since int64, beforeCreatedAt int64, beforeUniqueKey string, limit int) []OutboundMessage {
	if !messageStreamDatabaseReady() {
		return nil
	}
	if cfg.Type == "pg" {
		query := "SELECT source,link_id,root_comment_id,reply_comment_id,comment_id,text,image_url,created_at,raw_response,unique_key FROM outbound_messages WHERE created_at >= $1"
		args := []any{since}
		if beforeCreatedAt > 0 {
			query += " AND (created_at < $2 OR (created_at = $2 AND COALESCE(unique_key,'') < $3))"
			args = append(args, beforeCreatedAt, strings.TrimSpace(beforeUniqueKey))
		}
		query += " ORDER BY created_at DESC, unique_key DESC"
		if limit > 0 {
			args = append(args, limit)
			query += fmt.Sprintf(" LIMIT $%d", len(args))
		}
		rows, err := pg.Conn.Query(context.Background(), query, args...)
		if err != nil {
			loger.Loger.Warn("[DB]无法读取发出消息", zap.Error(err))
			return nil
		}
		defer rows.Close()
		return scanOutboundRows(rows)
	}
	if cfg.Type == "sqlite" {
		query := "SELECT source,link_id,root_comment_id,reply_comment_id,comment_id,text,image_url,created_at,raw_response,unique_key FROM outbound_messages WHERE created_at >= ?"
		args := []any{since}
		if beforeCreatedAt > 0 {
			query += " AND (created_at < ? OR (created_at = ? AND COALESCE(unique_key,'') < ?))"
			args = append(args, beforeCreatedAt, beforeCreatedAt, strings.TrimSpace(beforeUniqueKey))
		}
		query += " ORDER BY created_at DESC, unique_key DESC"
		if limit > 0 {
			query += " LIMIT ?"
			args = append(args, limit)
		}
		rows, err := sqlite.Db.Query(query, args...)
		if err != nil {
			loger.Loger.Warn("[DB]无法读取发出消息", zap.Error(err))
			return nil
		}
		defer rows.Close()
		return scanOutboundRows(rows)
	}
	return nil
}

func UpdateOutboundMessageComment(uniqueKey string, commentID int64, rootCommentID int64) bool {
	if !messageStreamDatabaseReady() {
		return false
	}
	if strings.TrimSpace(uniqueKey) == "" || commentID <= 0 {
		return false
	}
	ctx := context.Background()
	if cfg.Type == "pg" {
		_, err := pg.Conn.Exec(ctx, "UPDATE outbound_messages SET comment_id=$1, root_comment_id=$2 WHERE unique_key=$3", commentID, rootCommentID, uniqueKey)
		return err == nil
	}
	if cfg.Type == "sqlite" {
		_, err := sqlite.Db.Exec("UPDATE outbound_messages SET comment_id=?, root_comment_id=? WHERE unique_key=?", commentID, rootCommentID, uniqueKey)
		return err == nil
	}
	return false
}

type rowsScanner interface {
	Next() bool
	Scan(dest ...any) error
}

func scanOutboundRows(rows rowsScanner) []OutboundMessage {
	records := []OutboundMessage{}
	for rows.Next() {
		var record OutboundMessage
		if err := rows.Scan(&record.Source, &record.LinkID, &record.RootCommentID, &record.ReplyCommentID, &record.CommentID, &record.Text, &record.ImageURL, &record.CreatedAt, &record.RawResponse, &record.UniqueKey); err != nil {
			loger.Loger.Warn("[DB]无法解析发出消息", zap.Error(err))
			continue
		}
		records = append(records, record)
	}
	return records
}

func messageStreamDatabaseReady() bool {
	if cfg.Type == "pg" {
		return pg.Conn != nil
	}
	if cfg.Type == "sqlite" {
		return sqlite.Db != nil
	}
	return false
}

func outboundUniqueKey(record OutboundMessage) string {
	if strings.TrimSpace(record.UniqueKey) != "" {
		return strings.TrimSpace(record.UniqueKey)
	}
	if record.CommentID > 0 {
		return fmt.Sprintf("out:comment:%d", record.CommentID)
	}
	return fmt.Sprintf("out:%s:%d:%d:%d:%d:%s", record.Source, record.LinkID, record.RootCommentID, record.ReplyCommentID, record.CreatedAt, record.Text)
}

func inboundUniqueKey(record InboundMessage) string {
	if strings.TrimSpace(record.UniqueKey) != "" {
		return strings.TrimSpace(record.UniqueKey)
	}
	if record.MessageID > 0 {
		return fmt.Sprintf("in:msg:%d", record.MessageID)
	}
	if record.CommentID > 0 {
		return fmt.Sprintf("in:comment:%d", record.CommentID)
	}
	return fmt.Sprintf("in:%s:%d:%d:%d:%d:%s", record.Source, record.LinkID, record.RootCommentID, record.ReplyCommentID, record.UserID, record.Text)
}
