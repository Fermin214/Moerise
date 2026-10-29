package moerise

type ClientLogin struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type ClientCreateNote struct {
	Content string `json:"content"`
}

type NotePage struct {
	Notes         []Note `json:"notes"`
	NextPageToken string `json:"next_page_token"`
}

type NoteList struct {
	State     string
	Order     string
	PageSize  int
	PageToken string
}
