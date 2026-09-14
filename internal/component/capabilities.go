package component

// ConsumesHelper is shared by bootstrap reconciliation and protected lifecycle APIs.
func ConsumesHelper(service, helper string) bool {
	if helper == "gryphon" {
		return service == "saturn" || service == "chronos"
	}
	if helper == "neptune" {
		switch service {
		case "kernel", "volt", "saturn", "chronos", "laboratory":
			return true
		}
	}
	return false
}
