package xhh

import (
	"regexp"
	"strings"
)

type MentionControl struct {
	CleanedText         string
	SemanticText        string
	TargetText          string
	HasExplicitTarget   bool
	WakeOnly            bool
	MentionTargetUserID int
}

const mentionNamePattern = `([^\s，,。.!！?？:：、@]{1,24})`

var mentionTokenPattern = regexp.MustCompile(`@[^\s，,。.!！?？:：、@]{1,24}`)
var mentionControlPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:并|，|,|。|、|\s)*(?:顺便|帮我|请|可以|能不能)?(?:给|让|发给|拿给)\s*(?:他|她|ta|TA|对方|那个人|这个人)?\s*(?:看|看看|查看|看下|来看)\s*@` + mentionNamePattern),
	regexp.MustCompile(`(?:并|，|,|。|、|\s)*(?:顺便|帮我|请|可以|能不能)?(?:给|让|发给|拿给)\s*@` + mentionNamePattern + `\s*(?:看|看看|查看|看下|来看)?`),
	regexp.MustCompile(`(?:并|，|,|。|、|\s)*(?:顺便|帮我|请|可以|能不能)?(?:艾特|提到|喊|叫|咬|抓)\s*@?` + mentionNamePattern + `(?:看|看看|查看|看下|来看|评价|一下)?`),
	regexp.MustCompile(`(?:并|，|,|。|、|\s)*(?:顺便|帮我|请|可以|能不能)\s*@` + mentionNamePattern + `(?:看|看看|查看|看下|来看|评价|一下)?`),
	regexp.MustCompile(`(?:并|，|,|。|、|\s)*(?:问问|告诉|回复|反驳|怼|喷|骂|夸|安慰)\s*@?` + mentionNamePattern + `(?:怎么看|怎么想|的观点|的说法|的评论|的话|看看|查看|看下|来看|评价|一下|一口)?`),
}

func ParseMentionControl(text string) MentionControl {
	normalized := NormalizeCommentText(text)
	semantic := normalizeSemanticMentionText(normalized)
	cleaned := normalized
	target := ""
	for _, pattern := range mentionControlPatterns {
		match := pattern.FindStringSubmatch(cleaned)
		if len(match) < 2 {
			continue
		}
		candidate := normalizeMentionControlTarget(match[1])
		if candidate == "" {
			continue
		}
		if target == "" {
			target = candidate
		}
		cleaned = pattern.ReplaceAllString(cleaned, " ")
	}
	cleaned = leadingMentionPattern.ReplaceAllString(cleaned, "")
	cleaned = mentionTokenPattern.ReplaceAllString(cleaned, " ")
	cleaned = normalizeControlTextSpacing(cleaned)
	return MentionControl{CleanedText: cleaned, SemanticText: semantic, TargetText: target, HasExplicitTarget: target != "", WakeOnly: semantic == ""}
}

func normalizeSemanticMentionText(text string) string {
	text = leadingMentionPattern.ReplaceAllString(text, "")
	return normalizeControlTextSpacing(text)
}

func mentionQuestionText(mention MentionControl) string {
	if mention.SemanticText != "" {
		return mention.SemanticText
	}
	return "用户只艾特了机器人，没有附加内容"
}

func normalizeControlTextSpacing(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	text = strings.Trim(text, "：:，,。.!！?？、 ")
	return strings.TrimSpace(text)
}
