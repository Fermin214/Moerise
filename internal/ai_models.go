package moerise

type AIRequest struct {
	Names    []string `json:"names,omitempty"`
	Question string   `json:"question,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	State    string   `json:"state,omitempty"`
	From     string   `json:"from,omitempty"`
	Until    string   `json:"until,omitempty"`
	Timeline bool     `json:"timeline,omitempty"`
	Stream   bool     `json:"stream,omitempty"`
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

type EvidenceSet struct {
	Items    []Evidence     `json:"items"`
	Omitted  []string       `json:"omitted"`
	Semantic SearchSemantic `json:"semantic"`
}
