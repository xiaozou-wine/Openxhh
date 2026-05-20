package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"openxhh/config"
	"strings"
	"time"
)

type chatCompletionMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

const chatCompletionAttempts = 3

func sendChatCompletion(ctx context.Context, model string, messages []chatCompletionMessage) (string, error) {
	if strings.TrimSpace(model) == "" {
		return "", errors.New("model is empty")
	}
	useResponses := useResponsesAPI(config.ConfigStruct.Ai.BaseUrl)
	payload, err := buildChatCompletionPayload(model, messages, useResponses)
	if err != nil {
		return "", err
	}

	var lastErr error
	for attempt := 1; attempt <= chatCompletionAttempts; attempt++ {
		content, err := sendChatCompletionOnce(ctx, payload, useResponses)
		if err == nil {
			return content, nil
		}
		lastErr = err
		if !shouldRetryChatCompletionError(err) || attempt == chatCompletionAttempts {
			return "", err
		}
		if err := waitForChatCompletionRetry(ctx, attempt); err != nil {
			return "", err
		}
	}
	return "", lastErr
}

func buildChatCompletionPayload(model string, messages []chatCompletionMessage, useResponses bool) ([]byte, error) {
	if useResponses {
		rawMessages := make([]any, 0, len(messages))
		for _, message := range messages {
			rawMessages = append(rawMessages, message)
		}
		input, err := toResponsesInput(rawMessages)
		if err != nil {
			return nil, err
		}
		body := responsesBodyStruct{
			Model:  model,
			Input:  input,
			Stream: false,
		}
		return json.Marshal(body)
	}

	body := struct {
		Model    string                  `json:"model"`
		Messages []chatCompletionMessage `json:"messages"`
		Stream   bool                    `json:"stream"`
	}{
		Model:    model,
		Messages: messages,
		Stream:   false,
	}
	return json.Marshal(body)
}

func sendChatCompletionOnce(ctx context.Context, payload []byte, useResponses bool) (string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", config.ConfigStruct.Ai.BaseUrl, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+config.ConfigStruct.Ai.Token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", chatCompletionStatusError{statusCode: resp.StatusCode, body: limitRefineString(string(data), 300)}
	}

	if useResponses {
		parsed, err := parseResponsesResp(data)
		if err != nil {
			return "", err
		}
		if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Msg.Content) == "" {
			return "", errors.New("chat completion response has no content")
		}
		return parsed.Choices[0].Msg.Content, nil
	}

	var parsed promptRefineResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", errors.New("chat completion response has no content")
	}
	return parsed.Choices[0].Message.Content, nil
}

type chatCompletionStatusError struct {
	statusCode int
	body       string
}

func (e chatCompletionStatusError) Error() string {
	return fmt.Sprintf("chat completion request failed: status=%d body=%s", e.statusCode, e.body)
}

func shouldRetryChatCompletionError(err error) bool {
	var statusErr chatCompletionStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.statusCode {
		case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}
	return errors.Is(err, io.ErrUnexpectedEOF)
}

func waitForChatCompletionRetry(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt) * 700 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
