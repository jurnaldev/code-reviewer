package gitlab

type MR struct {
	IID      int    `json:"iid"`
	Title    string `json:"title"`
	WebURL   string `json:"web_url"`
	BaseSHA  string
	StartSHA string
	HeadSHA  string
}

type FileChange struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	Diff        string `json:"diff"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
}

type Position struct {
	BaseSHA      string `json:"base_sha"`
	StartSHA     string `json:"start_sha"`
	HeadSHA      string `json:"head_sha"`
	NewPath      string `json:"new_path"`
	OldPath      string `json:"old_path"`
	NewLine      int    `json:"new_line,omitempty"`
	PositionType string `json:"position_type"` // "text"
}
