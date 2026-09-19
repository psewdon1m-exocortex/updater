package model

// Progress describes observed work. Unknown durations deliberately remain
// indeterminate; phase changes are not fabricated percentage estimates.
type Progress struct {
	Phase     string `json:"phase"`
	Mode      string `json:"mode"`
	Completed int64  `json:"completed,omitempty"`
	Total     int64  `json:"total,omitempty"`
}

func JobProgress(job Job) Progress {
	p := Progress{Phase: job.State, Mode: "indeterminate"}
	if job.FinishedAt != nil {
		p.Mode = "complete"
		p.Completed, p.Total = 1, 1
	}
	return p
}
