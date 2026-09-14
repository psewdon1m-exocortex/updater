package component

import "testing"

func TestHeadHelperCapabilities(t *testing.T) {
	for _, service := range []string{"kernel", "volt", "saturn", "chronos", "laboratory", "unknown"} {
		if ConsumesHelper(service, "neptune") != (service != "unknown") {
			t.Fatalf("Neptune capability: %s", service)
		}
		if ConsumesHelper(service, "gryphon") != (service == "saturn" || service == "chronos") {
			t.Fatalf("Gryphon capability: %s", service)
		}
		if ConsumesHelper(service, "arbitrary") {
			t.Fatal("unknown helper accepted")
		}
	}
}
