package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxResponseSize = 10 * 1024 * 1024 // 10MB

func redactTokenError(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), token, "***"))
}

func telegramGet(token string, url string) (*http.Response, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, redactTokenError(err, token)
	}
	return resp, nil
}

func telegramClientGet(client *http.Client, token string, url string) (*http.Response, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, redactTokenError(err, token)
	}
	return resp, nil
}

func telegramAPI(config *Config, method string, params url.Values) (*TelegramResponse, error) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/%s", config.BotToken, method)
	resp, err := http.PostForm(apiURL, params)
	if err != nil {
		return nil, redactTokenError(err, config.BotToken)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	var result TelegramResponse
	json.Unmarshal(body, &result)
	return &result, nil
}

func addThreadID(params url.Values, threadID int) {
	if threadID > 0 {
		params.Set("message_thread_id", fmt.Sprintf("%d", threadID))
	}
}

func sendMessage(config *Config, chatID int64, text string, threadID ...int) error {
	tid := 0
	if len(threadID) > 0 {
		tid = threadID[0]
	}
	const maxLen = 4000

	messages := splitMessage(text, maxLen)

	for _, msg := range messages {
		// Try with Markdown first
		params := url.Values{
			"chat_id":    {fmt.Sprintf("%d", chatID)},
			"text":       {msg},
			"parse_mode": {"Markdown"},
		}
		addThreadID(params, tid)

		result, err := telegramAPI(config, "sendMessage", params)
		if err != nil {
			return err
		}
		if !result.OK {
			// Retry without formatting after 500ms
			time.Sleep(500 * time.Millisecond)
			params = url.Values{
				"chat_id": {fmt.Sprintf("%d", chatID)},
				"text":    {msg},
			}
			addThreadID(params, tid)
			result, err = telegramAPI(config, "sendMessage", params)
			if err != nil {
				return err
			}
			if !result.OK {
				return fmt.Errorf("telegram error: %s", result.Description)
			}
		}

		if len(messages) > 1 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return nil
}

func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var messages []string
	remaining := text

	for len(remaining) > 0 {
		if len(remaining) <= maxLen {
			messages = append(messages, remaining)
			break
		}

		splitAt := maxLen

		if idx := strings.LastIndex(remaining[:maxLen], "\n"); idx > maxLen/2 {
			splitAt = idx + 1
		} else if idx := strings.LastIndex(remaining[:maxLen], " "); idx > maxLen/2 {
			splitAt = idx + 1
		}

		messages = append(messages, strings.TrimRight(remaining[:splitAt], " \n"))
		remaining = remaining[splitAt:]
	}

	return messages
}

func sendMessageGetID(config *Config, chatID int64, text string, threadID ...int) (int, error) {
	tid := 0
	if len(threadID) > 0 {
		tid = threadID[0]
	}
	params := url.Values{
		"chat_id":    {fmt.Sprintf("%d", chatID)},
		"text":       {text},
		"parse_mode": {"Markdown"},
	}
	addThreadID(params, tid)
	result, err := telegramAPI(config, "sendMessage", params)
	if err != nil {
		return 0, err
	}
	if !result.OK {
		params = url.Values{
			"chat_id": {fmt.Sprintf("%d", chatID)},
			"text":    {text},
		}
		addThreadID(params, tid)
		result, err = telegramAPI(config, "sendMessage", params)
		if err != nil {
			return 0, err
		}
		if !result.OK {
			return 0, fmt.Errorf("telegram error: %s", result.Description)
		}
	}
	var msg struct {
		MessageID int `json:"message_id"`
	}
	json.Unmarshal(result.Result, &msg)
	return msg.MessageID, nil
}

func sendMessageWithKeyboard(config *Config, chatID int64, text string, keyboard interface{}, threadID ...int) (int, error) {
	tid := 0
	if len(threadID) > 0 {
		tid = threadID[0]
	}
	kbJSON, err := json.Marshal(keyboard)
	if err != nil {
		return 0, err
	}

	params := url.Values{
		"chat_id":      {fmt.Sprintf("%d", chatID)},
		"text":         {text},
		"reply_markup": {string(kbJSON)},
	}
	addThreadID(params, tid)

	result, err := telegramAPI(config, "sendMessage", params)
	if err != nil {
		return 0, err
	}
	if !result.OK {
		return 0, fmt.Errorf("telegram error: %s", result.Description)
	}

	var msg struct {
		MessageID int `json:"message_id"`
	}
	json.Unmarshal(result.Result, &msg)
	return msg.MessageID, nil
}

func answerCallbackQuery(config *Config, callbackID string) {
	telegramAPI(config, "answerCallbackQuery", url.Values{
		"callback_query_id": {callbackID},
	})
}

func deleteMessage(config *Config, chatID int64, messageID int) {
	telegramAPI(config, "deleteMessage", url.Values{
		"chat_id":    {fmt.Sprintf("%d", chatID)},
		"message_id": {fmt.Sprintf("%d", messageID)},
	})
}

func editMessageReplyMarkup(config *Config, chatID int64, messageID int) {
	telegramAPI(config, "editMessageReplyMarkup", url.Values{
		"chat_id":      {fmt.Sprintf("%d", chatID)},
		"message_id":   {fmt.Sprintf("%d", messageID)},
		"reply_markup": {`{"inline_keyboard":[]}`},
	})
}

func sendFile(config *Config, chatID int64, filePath string, caption string) error {
	return sendFileMultipart(config, chatID, filePath, caption, "document", "sendDocument")
}

func sendPhoto(config *Config, chatID int64, filePath string, caption string) error {
	return sendFileMultipart(config, chatID, filePath, caption, "photo", "sendPhoto")
}

func sendVideo(config *Config, chatID int64, filePath string, caption string) error {
	return sendFileMultipart(config, chatID, filePath, caption, "video", "sendVideo")
}

func sendFileMultipart(config *Config, chatID int64, filePath string, caption string, fieldName string, apiMethod string, threadID ...int) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	writer.WriteField("chat_id", fmt.Sprintf("%d", chatID))
	if len(threadID) > 0 && threadID[0] > 0 {
		writer.WriteField("message_thread_id", fmt.Sprintf("%d", threadID[0]))
	}
	if caption != "" {
		writer.WriteField("caption", caption)
	}

	part, err := writer.CreateFormFile(fieldName, filepath.Base(filePath))
	if err != nil {
		return err
	}
	io.Copy(part, file)
	writer.Close()

	resp, err := http.Post(
		fmt.Sprintf("https://api.telegram.org/bot%s/%s", config.BotToken, apiMethod),
		writer.FormDataContentType(),
		body,
	)
	if err != nil {
		return redactTokenError(err, config.BotToken)
	}
	defer resp.Body.Close()

	var result TelegramResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if !result.OK {
		return fmt.Errorf("telegram error: %s", result.Description)
	}
	return nil
}

func downloadTelegramFile(config *Config, fileID string, destPath string) error {
	resp, err := telegramGet(config.BotToken, fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", config.BotToken, fileID))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("failed to get file path")
	}

	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", config.BotToken, result.Result.FilePath)
	fileResp, err := telegramGet(config.BotToken, fileURL)
	if err != nil {
		return err
	}
	defer fileResp.Body.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, fileResp.Body)
	return err
}

func sendChatAction(config *Config, chatID int64, action string, threadID ...int) {
	params := url.Values{
		"chat_id": {fmt.Sprintf("%d", chatID)},
		"action":  {action},
	}
	if len(threadID) > 0 {
		addThreadID(params, threadID[0])
	}
	telegramAPI(config, "sendChatAction", params)
}

func editMessageText(config *Config, chatID int64, messageID int, text string, threadID ...int) error {
	params := url.Values{
		"chat_id":    {fmt.Sprintf("%d", chatID)},
		"message_id": {fmt.Sprintf("%d", messageID)},
		"text":       {text},
		"parse_mode": {"Markdown"},
	}
	result, err := telegramAPI(config, "editMessageText", params)
	if err != nil {
		return err
	}
	if !result.OK {
		if strings.Contains(result.Description, "message is not modified") {
			return nil
		}
		params = url.Values{
			"chat_id":    {fmt.Sprintf("%d", chatID)},
			"message_id": {fmt.Sprintf("%d", messageID)},
			"text":       {text},
		}
		result, err = telegramAPI(config, "editMessageText", params)
		if err != nil {
			return err
		}
		if !result.OK {
			if strings.Contains(result.Description, "message is not modified") {
				return nil
			}
			return fmt.Errorf("telegram error: %s", result.Description)
		}
	}
	return nil
}

func createForumTopic(config *Config, chatID int64, name string) (int, error) {
	params := url.Values{
		"chat_id": {fmt.Sprintf("%d", chatID)},
		"name":    {name},
	}
	result, err := telegramAPI(config, "createForumTopic", params)
	if err != nil {
		return 0, err
	}
	if !result.OK {
		return 0, fmt.Errorf("telegram error: %s", result.Description)
	}
	var topic struct {
		MessageThreadID int `json:"message_thread_id"`
	}
	if err := json.Unmarshal(result.Result, &topic); err != nil {
		return 0, err
	}
	return topic.MessageThreadID, nil
}

func setBotCommands(botToken string) {
	commands := []map[string]string{
		{"command": "c", "description": "Execute shell command: /c <cmd>"},
		{"command": "restart", "description": "Restart Claude session"},
		{"command": "topic", "description": "Create topic session: /topic <name> <folder>"},
		{"command": "version", "description": "Show ccc version"},
		{"command": "stats", "description": "Show system stats"},
	}

	body, _ := json.Marshal(map[string]interface{}{
		"commands": commands,
	})
	resp, err := http.Post(
		fmt.Sprintf("https://api.telegram.org/bot%s/setMyCommands", botToken),
		"application/json",
		bytes.NewReader(body),
	)
	if err == nil {
		resp.Body.Close()
	}
}
