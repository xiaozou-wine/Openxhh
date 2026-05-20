package xhh

import (
	"openxhh/config"
	"openxhh/db"
	"testing"
)

func resetReplySchedulerState(t *testing.T) {
	t.Helper()
	oldOwner := config.ConfigStruct.Xhh.Owner
	oldOwners := append([]int(nil), Owners...)
	oldMaxReplyThreads := MaxReplyThreads
	oldMaxPendingReplies := MaxPendingReplies
	t.Cleanup(func() {
		config.ConfigStruct.Xhh.Owner = oldOwner
		Owners = oldOwners
		MaxReplyThreads = oldMaxReplyThreads
		MaxPendingReplies = oldMaxPendingReplies
	})
}

func TestSelectReplyBatchLimitsNormalUsersToOne(t *testing.T) {
	resetReplySchedulerState(t)
	config.ConfigStruct.Xhh.Owner = "100"
	Owners = nil
	MaxReplyThreads = 3

	got := selectReplyBatch([]db.CommStruct{
		{MsgID: 1, Uid: 200},
		{MsgID: 2, Uid: 201},
		{MsgID: 3, Uid: 202},
	})

	if len(got) != 1 {
		t.Fatalf("len(selectReplyBatch) = %d, want 1", len(got))
	}
	if got[0].MsgID != 1 {
		t.Fatalf("selected MsgID = %d, want 1", got[0].MsgID)
	}
	if workers := replyWorkerCount(got); workers != 1 {
		t.Fatalf("replyWorkerCount = %d, want 1", workers)
	}
}

func TestSelectReplyBatchUsesThreadLimitForOwners(t *testing.T) {
	resetReplySchedulerState(t)
	config.ConfigStruct.Xhh.Owner = "100"
	Owners = nil
	MaxReplyThreads = 2

	got := selectReplyBatch([]db.CommStruct{
		{MsgID: 1, Uid: 200},
		{MsgID: 2, Uid: 100},
		{MsgID: 3, Uid: 100},
		{MsgID: 4, Uid: 100},
	})

	if len(got) != 2 {
		t.Fatalf("len(selectReplyBatch) = %d, want 2", len(got))
	}
	for _, item := range got {
		if item.Uid != 100 {
			t.Fatalf("selected non-owner reply: %+v", item)
		}
	}
	if workers := replyWorkerCount(got); workers != 2 {
		t.Fatalf("replyWorkerCount = %d, want 2", workers)
	}
}

func TestReplyCandidateLimitIncludesNormalQueueAndOwnerThreads(t *testing.T) {
	resetReplySchedulerState(t)
	MaxPendingReplies = 50
	MaxReplyThreads = 3

	if got := replyCandidateLimit(); got != 53 {
		t.Fatalf("replyCandidateLimit = %d, want 53", got)
	}
}
