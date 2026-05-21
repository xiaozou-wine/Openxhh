package ai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"openxhh/config"
	"openxhh/loger"
	"strings"

	"go.uber.org/zap"
)

type Content struct {
	Type   string `json:"type"`
	ImgUrl struct {
		Url string `json:"url"`
	} `json:"image_url"`
	Text string `json:"text"`
}
type Messages[T []Content | string] struct {
	Role    string `json:"role"`
	Content T      `json:"content"`
}
type SysMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type BodyStruct struct {
	Model            string            `json:"model"`
	Msgs             []any             `json:"messages,omitempty"`
	Stream           bool              `json:"stream"`
	WebSearchOptions *webSearchOptions `json:"web_search_options,omitempty"`
}

type webSearchOptions struct {
	SearchContextSize string `json:"search_context_size,omitempty"`
}

type responsesBodyStruct struct {
	Model        string             `json:"model"`
	Instructions string             `json:"instructions,omitempty"`
	Input        any                `json:"input"`
	Tools        []responsesWebTool `json:"tools,omitempty"`
	ToolChoice   string             `json:"tool_choice,omitempty"`
	Stream       bool               `json:"stream"`
}

type responsesInputMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type responsesInputContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

const responsesWebSearchToolType = "web_search_preview"
const legacyResponsesWebSearchToolType = "web_search"

type responsesWebTool struct {
	Type              string `json:"type"`
	SearchContextSize string `json:"search_context_size,omitempty"`
}

type choice struct {
	Index int `json:"index"`
	Msg   struct {
		Role    string `json:"role"`
		Content string `json:"content"`
		Reason  string `json:"reasoning_content"`
	} `json:"message"`
}

type respStruct struct {
	Choices []choice `json:"choices"`
	Usage   struct {
		TotalToken int `json:"total_tokens"`
	} `json:"usage"`
}

type rawMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type aiRequestPayload struct {
	Name string
	Body []byte
}

type responsesRespStruct struct {
	OutputText string `json:"output_text"`
	Output     []struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		TotalToken int `json:"total_tokens"`
	} `json:"usage"`
}

func SendReq(Model string, Msg []any) (Jresp respStruct) {
	if Model == "" {
		loger.Loger.Fatal("[Ai]请确保配置文件中的模型是存在的")
	}
	cfg := config.ConfigStruct.Ai
	payloads, err := buildReqPayloads(Model, Msg)
	if err != nil {
		loger.Loger.Error("[AI]无法序列化JSON", zap.Error(err))
		return
	}
	for i, payload := range payloads {
		req, err := http.NewRequest("POST", cfg.BaseUrl, bytes.NewReader(payload.Body))
		if err != nil {
			loger.Loger.Error("[AI]无法创建请求", zap.Error(err), zap.String("variant", payload.Name))
			return
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		req.Header.Set("Content-Type", "application/json")
		client := http.DefaultClient
		resp, err := client.Do(req)
		if err != nil {
			loger.Loger.Error("[AI]请求失败！", zap.Error(err), zap.String("variant", payload.Name))
			return
		}

		Dresp, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			loger.Loger.Error("[AI]无法读取响应", zap.Error(readErr), zap.String("variant", payload.Name))
			return
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			if i < len(payloads)-1 && shouldTryNextResponsesPayload(resp.StatusCode) {
				loger.Loger.Warn("[Ai]Responses请求失败，尝试兼容格式", zap.String("variant", payload.Name), zap.Int("status", resp.StatusCode), zap.String("body", limitRefineString(string(Dresp), 500)))
				continue
			}
			loger.Loger.Error("[Ai]Ai返回HTTP错误", zap.String("variant", payload.Name), zap.Int("status", resp.StatusCode), zap.String("body", limitRefineString(string(Dresp), 500)))
			return
		}
		if useResponsesAPI(cfg.BaseUrl) {
			Jresp, err = parseResponsesResp(Dresp)
		} else {
			err = json.Unmarshal(Dresp, &Jresp)
		}
		if err != nil {
			loger.Loger.Error("[Ai]无法反序列化JSON", zap.Error(err), zap.String("variant", payload.Name), zap.String("body", string(Dresp)))
			return
		}
		if len(Jresp.Choices) == 0 {
			loger.Loger.Error("[Ai]Ai返回错误", zap.String("variant", payload.Name), zap.Any("Resp", string(Dresp)))
			return
		}
		return Jresp
	}
	return Jresp
}

func buildReqBody(Model string, Msg []any) ([]byte, error) {
	payloads, err := buildReqPayloads(Model, Msg)
	if err != nil || len(payloads) == 0 {
		return nil, err
	}
	return payloads[0].Body, nil
}

func buildReqPayloads(Model string, Msg []any) ([]aiRequestPayload, error) {
	cfg := config.ConfigStruct.Ai
	if useResponsesAPI(cfg.BaseUrl) {
		primary, err := buildResponsesReqBody(Model, Msg, false)
		if err != nil {
			return nil, err
		}
		legacy, err := buildResponsesReqBody(Model, Msg, true)
		if err != nil {
			return nil, err
		}
		return []aiRequestPayload{{Name: "responses", Body: primary}, {Name: "responses_compat", Body: legacy}}, nil
	}

	body := BodyStruct{
		Model:  Model,
		Msgs:   Msg,
		Stream: false,
	}
	if aiWebSearchEnabled() {
		body.WebSearchOptions = &webSearchOptions{SearchContextSize: aiSearchContextSize()}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return []aiRequestPayload{{Name: "chat_completions", Body: data}}, nil
}

func buildResponsesReqBody(Model string, Msg []any, legacy bool) ([]byte, error) {
	return buildResponsesReqBodyWithTools(Model, Msg, legacy, aiWebSearchEnabled())
}

func buildResponsesReqBodyWithTools(Model string, Msg []any, legacy bool, includeWebSearch bool) ([]byte, error) {
	var body responsesBodyStruct
	if legacy {
		input, err := toResponsesInput(Msg)
		if err != nil {
			return nil, err
		}
		body = responsesBodyStruct{
			Model:  Model,
			Input:  input,
			Stream: false,
		}
		if includeWebSearch {
			body.Tools = []responsesWebTool{{Type: legacyResponsesWebSearchToolType, SearchContextSize: aiSearchContextSize()}}
			if aiForceWebSearchEnabled() {
				body.ToolChoice = "required"
			}
		}
		return json.Marshal(body)
	}

	instructions, input, err := toResponsesPayloadParts(Msg)
	if err != nil {
		return nil, err
	}
	body = responsesBodyStruct{
		Model:        Model,
		Instructions: instructions,
		Input:        input,
		Stream:       false,
	}
	if includeWebSearch {
		body.Tools = []responsesWebTool{{Type: responsesWebSearchToolType, SearchContextSize: aiSearchContextSize()}}
		if aiForceWebSearchEnabled() {
			body.ToolChoice = "required"
		}
	}
	return json.Marshal(body)
}

func shouldTryNextResponsesPayload(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusUnprocessableEntity || status == http.StatusBadGateway
}

func useResponsesAPI(baseURL string) bool {
	return strings.Contains(strings.ToLower(baseURL), "/responses")
}

func aiWebSearchEnabled() bool {
	value := config.ConfigStruct.Ai.WebSearch
	return value == nil || *value
}

func aiForceWebSearchEnabled() bool {
	value := config.ConfigStruct.Ai.ForceWebSearch
	return value != nil && *value
}

func aiSearchContextSize() string {
	size := strings.ToLower(strings.TrimSpace(config.ConfigStruct.Ai.SearchContextSize))
	switch size {
	case "low", "medium", "high":
		return size
	default:
		return "medium"
	}
}

func toResponsesPayloadParts(Msg []any) (string, []responsesInputMsg, error) {
	instructions := make([]string, 0, 1)
	input := make([]responsesInputMsg, 0, len(Msg))
	for _, msg := range Msg {
		data, err := json.Marshal(msg)
		if err != nil {
			return "", nil, err
		}
		var raw rawMsg
		if err := json.Unmarshal(data, &raw); err != nil {
			return "", nil, err
		}
		role := strings.ToLower(strings.TrimSpace(raw.Role))
		if role == "system" || role == "developer" {
			instruction, err := responsesInstructionText(raw.Content)
			if err != nil {
				return "", nil, err
			}
			if instruction != "" {
				instructions = append(instructions, instruction)
			}
			continue
		}
		if role == "" {
			role = "user"
		}
		content, err := toResponsesContent(raw.Content)
		if err != nil {
			return "", nil, err
		}
		input = append(input, responsesInputMsg{
			Role:    role,
			Content: content,
		})
	}
	return strings.Join(instructions, "\n\n"), input, nil
}

func toResponsesInput(Msg []any) ([]responsesInputMsg, error) {
	input := make([]responsesInputMsg, 0, len(Msg))
	for _, msg := range Msg {
		data, err := json.Marshal(msg)
		if err != nil {
			return nil, err
		}
		var raw rawMsg
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		role := strings.ToLower(strings.TrimSpace(raw.Role))
		if role == "system" {
			role = "developer"
		}
		if role == "" {
			role = "user"
		}
		content, err := toResponsesContent(raw.Content)
		if err != nil {
			return nil, err
		}
		input = append(input, responsesInputMsg{
			Role:    role,
			Content: content,
		})
	}
	return input, nil
}

func responsesInstructionText(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text), nil
	}

	var contents []Content
	if err := json.Unmarshal(raw, &contents); err != nil {
		return "", err
	}
	parts := make([]string, 0, len(contents))
	for _, content := range contents {
		text := strings.TrimSpace(content.Text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func toResponsesContent(raw json.RawMessage) (any, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []responsesInputContent{{Type: "input_text", Text: text}}, nil
	}

	var contents []Content
	if err := json.Unmarshal(raw, &contents); err != nil {
		return nil, err
	}
	responseContents := make([]responsesInputContent, 0, len(contents))
	for _, content := range contents {
		switch content.Type {
		case "text":
			responseContents = append(responseContents, responsesInputContent{Type: "input_text", Text: content.Text})
		case "image_url":
			if content.ImgUrl.Url != "" {
				responseContents = append(responseContents, responsesInputContent{Type: "input_image", ImageURL: content.ImgUrl.Url})
			}
		default:
			if content.Text != "" {
				responseContents = append(responseContents, responsesInputContent{Type: "input_text", Text: content.Text})
			}
		}
	}
	return responseContents, nil
}

func parseResponsesResp(data []byte) (respStruct, error) {
	var responsesResp responsesRespStruct
	if err := json.Unmarshal(data, &responsesResp); err != nil {
		return respStruct{}, err
	}

	text := responsesResp.OutputText
	if text == "" {
		var builder strings.Builder
		for _, output := range responsesResp.Output {
			if output.Type != "" && output.Type != "message" {
				continue
			}
			for _, content := range output.Content {
				if content.Text == "" {
					continue
				}
				if content.Type != "" && content.Type != "output_text" && content.Type != "text" {
					continue
				}
				if builder.Len() > 0 {
					builder.WriteString("\n")
				}
				builder.WriteString(content.Text)
			}
		}
		text = builder.String()
	}
	if text == "" {
		return respStruct{}, nil
	}

	var resp respStruct
	resp.Choices = []choice{{Index: 0}}
	resp.Choices[0].Msg.Role = "assistant"
	resp.Choices[0].Msg.Content = text
	resp.Usage.TotalToken = responsesResp.Usage.TotalToken
	return resp, nil
}
