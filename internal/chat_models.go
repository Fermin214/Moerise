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
	Usage           LLMUsage       `json:"usage"`
}

type ChatSummary struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Version       int64  `json:"version"`
	MaterialCount int    `json:"material_count"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type ChatSession struct {
	ChatSummary
	Materials []ChatMaterial `json:"materials"`
	Turns     []ChatTurn     `json:"turns"`
}

type ChatCreate struct {
	RequestID string   `json:"request_id"`
	Title     string   `json:"title"`
	Names     []string `json:"names"`
}

type ChatUpdate struct {
	Version int64    `json:"version"`
	Title   string   `json:"title"`
	Names   []string `json:"names"`
}

type ChatGenerate struct {
	RequestID string `json:"request_id"`
	Version   int64  `json:"version"`
	Kind      string `json:"kind"`
	Content   string `json:"content"`
}

type chatSave struct {
	Key, Name string
	Version   int64
}

const ChatCharacterLimit = 20000

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
