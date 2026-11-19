package moerise

// ChatMaterial 只保存来源引用；生成时重新通过官方 API 读取原文。
type ChatMaterial struct {
	Name       string `json:"name"`
	SourceHash string `json:"source_hash"`
}

type ChatMessage struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Version   int64  `json:"version"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code"`
	SavedName string `json:"saved_name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type ChatTurn struct {
	ID              string         `json:"id"`
	RequestID       string         `json:"request_id"`
	Kind            string         `json:"kind"`
	User            ChatMessage    `json:"user"`
	Assistant       ChatMessage    `json:"assistant"`
	SettingsVersion int64          `json:"settings_version"`
	Prompt          string         `json:"prompt"`
	Model           string         `json:"model"`
	Materials       []ChatMaterial `json:"materials"`
	MaterialVersion int64          `json:"material_version"`
	Usage           LLMUsage       `json:"usage"`
}

type ChatSummary struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Preview         string `json:"preview"`
	PreviewKind     string `json:"preview_kind"`
	Version         int64  `json:"version"`
	MaterialCount   int    `json:"material_count"`
	MaterialVersion int64  `json:"material_version"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type ChatSession struct {
	ChatSummary
	Materials []ChatMaterial `json:"materials"`
	Turns     []ChatTurn     `json:"turns"`
}

type ChatCreate struct {
	RequestID string         `json:"request_id"`
	Title     string         `json:"title"`
	Names     []string       `json:"names,omitempty"`
	Materials []ChatMaterial `json:"materials,omitempty"`
}

type ChatUpdate struct {
	Version   int64          `json:"version"`
	Title     string         `json:"title"`
	Names     []string       `json:"names,omitzero"`
	Materials []ChatMaterial `json:"materials,omitzero"`
}

type ChatGenerate struct {
	RequestID string `json:"request_id"`
	Version   int64  `json:"version"`
	Kind      string `json:"kind"`
	Content   string `json:"content"`
	Language  string `json:"language,omitempty"`
}

type chatSave struct {
	Key, Name string
	Version   int64
}

const ChatCharacterLimit = 20000
const ChatSourceLimit = 1000

type ChatCandidate struct {
	ChatMaterial
	Characters int    `json:"characters"`
	Preview    string `json:"preview"`
	CreateTime string `json:"create_time"`
	State      string `json:"state"`
}

type ChatMessageRef struct {
	RequestID string `json:"request_id"`
	Role      string `json:"role"`
	Version   int64  `json:"version"`
}

type ChatSaveSelection struct {
	OperationKey      string           `json:"operation_key"`
	Language          string           `json:"language"`
	Messages          []ChatMessageRef `json:"messages"`
	ConfirmIncomplete bool             `json:"confirm_incomplete"`
}

type ChatSaveReceipt struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type ChatMaterials struct {
	Notes      []Note         `json:"notes"`
	Materials  []ChatMaterial `json:"materials"`
	Characters int            `json:"characters"`
	Limit      int            `json:"limit"`
}

type ChatEvent struct {
	Type string    `json:"type"`
	Turn *ChatTurn `json:"turn,omitempty"`
	Text string    `json:"text,omitempty"`
}
