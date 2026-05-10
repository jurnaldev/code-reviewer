package mem9

type Hit struct {
	ID       string            `json:"id"`
	Content  string            `json:"content"`
	Score    float64           `json:"score"`
	Tags     []string          `json:"tags"`
	Metadata map[string]string `json:"metadata"`
}

type CreateInput struct {
	Content  string
	Tags     []string
	Metadata map[string]string
}

type SearchInput struct {
	Query  string
	Tags   []string
	Mode   string // "hybrid" | "semantic" | "keyword"
	Limit  int
	Offset int
}
