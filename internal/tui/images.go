package tui

import (
	"fmt"

	"updater/internal/console"
)

func imagePlanLines(plan console.ImagePlan) []string {
	lines := []string{fmt.Sprintf("Exocortex images: %d; protected: %d", plan.OwnedImages, plan.ProtectedImages),
		fmt.Sprintf("Candidates in this batch: %d; later batches: %d", len(plan.Candidates), plan.Remaining),
		"Current containers, the previous offline generation and recovery jobs remain protected.", ""}
	for _, item := range plan.Candidates {
		id := item.ID
		if len(id) > 19 {
			id = id[:19]
		}
		lines = append(lines, fmt.Sprintf("%s  %s  %s", item.Repository, id, item.Size))
	}
	if len(plan.Candidates) > 0 {
		lines = append(lines, "", "Enter: review cleanup confirmation. Esc: leave without deleting.")
	} else {
		lines = append(lines, "No eligible images. No cleanup is needed.")
	}
	return lines
}

func imageCleanLines(result console.ImageCleanResult) []string {
	return []string{fmt.Sprintf("Removed %d reviewed Docker images. Open storage again to inspect remaining candidates.", len(result.Removed))}
}
