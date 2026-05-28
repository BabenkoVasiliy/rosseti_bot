package rosseti

type ShutdownRecord struct {
	ID         string `json:"id"`
	Region     string `json:"region"`
	Raion      string `json:"raion"`
	Gorod      string `json:"gorod"`
	Street     string `json:"street"`
	DateStart  string `json:"date_start"`
	DateFinish string `json:"date_finish"`
	FOtkl      string `json:"f_otkl"`
	Res        string `json:"res"`
	TimeStart  string `json:"time_start"`
	TimeFinish string `json:"time_finish"`
}

type Region struct {
	Value string `json:"value"`
	Title string `json:"title"`
}

type OutagesPayload struct {
	Outages []ShutdownRecord `json:"outages"`
}
