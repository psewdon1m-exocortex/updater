package console

import (
	"context"
	"time"
)

type ImageItem struct {
	ID         string    `json:"id"`
	Repository string    `json:"repository"`
	CreatedAt  time.Time `json:"created_at"`
	Size       string    `json:"size"`
}

type ImagePlan struct {
	ID              string      `json:"id"`
	ObservedAt      time.Time   `json:"observed_at"`
	OwnedImages     int         `json:"owned_images"`
	ProtectedImages int         `json:"protected_images"`
	Candidates      []ImageItem `json:"candidates"`
	Remaining       int         `json:"remaining"`
}

type ImageCleanResult struct {
	Removed []string `json:"removed"`
}

type ImageBackend interface {
	ImagePlan(context.Context) (ImagePlan, error)
	CleanImages(context.Context, string) (ImageCleanResult, error)
}
