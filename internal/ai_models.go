package moerise

// Historical wire types remain readable while queued channel results recover.
type AIStatement struct {
	Kind      string   `json:"kind"`
	Text      string   `json:"text"`
	Citations []string `json:"citations"`
}

type AIMetrics struct {
	RetrievalMS    int64  `json:"retrieval_ms"`
	FirstContentMS *int64 `json:"first_content_ms"`
	GenerationMS   int64  `json:"generation_ms"`
	TotalMS        int64  `json:"total_ms"`
}

type AIResult struct {
	Kind        string             `json:"kind"`
	Status      string             `json:"status"`
	Completed   bool               `json:"completed"`
	Statements  []AIStatement      `json:"statements"`
	References  []Evidence         `json:"references"`
	UsedSources []string           `json:"used_sources"`
	Omitted     []string           `json:"omitted"`
	Missing     []string           `json:"missing"`
	Timeline    []AITimelinePeriod `json:"timeline"`
	Semantic    SearchSemantic     `json:"semantic"`
	Model       string             `json:"model"`
	Usage       LLMUsage           `json:"usage"`
	Metrics     AIMetrics          `json:"metrics"`
}

type AITimelinePeriod struct {
	ID                  string         `json:"id"`
	From                string         `json:"from"`
	Until               string         `json:"until"`
	RepresentativeNames []string       `json:"representative_names"`
	StatementIndexes    []int          `json:"statement_indexes"`
	Empty               bool           `json:"empty"`
	MissingReason       string         `json:"missing_reason,omitempty"`
	Semantic            SearchSemantic `json:"semantic"`
}

type Evidence struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SourceHash string `json:"source_hash"`
	ByteStart  int    `json:"byte_start"`
	ByteEnd    int    `json:"byte_end"`
	LineStart  int    `json:"line_start"`
	LineEnd    int    `json:"line_end"`
	Content    string `json:"content"`
	URL        string `json:"url"`
	CreateTime string `json:"create_time"`
	UpdateTime string `json:"update_time"`
}
