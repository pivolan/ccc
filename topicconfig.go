package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type TopicSession struct {
	GroupID    int64  `json:"group_id"`
	TopicID    int    `json:"topic_id"`
	FolderPath string `json:"folder_path"`
}

func topicConfigPath(workDir string) string {
	return filepath.Join(workDir, ".ccc-topics.json")
}

func topicKey(groupID int64, topicID int) string {
	return fmt.Sprintf("%d:%d", groupID, topicID)
}

func topicSessionName(groupID int64, topicID int) string {
	return fmt.Sprintf("ccc-%d-%d", groupID, topicID)
}

func loadTopicConfig(workDir string) map[string]TopicSession {
	data, err := os.ReadFile(topicConfigPath(workDir))
	if err != nil {
		return make(map[string]TopicSession)
	}
	var m map[string]TopicSession
	if json.Unmarshal(data, &m) != nil {
		return make(map[string]TopicSession)
	}
	return m
}

func saveTopicConfig(workDir string, m map[string]TopicSession) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(topicConfigPath(workDir), data, 0644)
}
