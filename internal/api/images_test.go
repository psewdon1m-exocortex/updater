package api

import (
	"net/http/httptest"
	"testing"
)

func TestImageCleanupRoutesAreOperatorOnly(t *testing.T) {
	server := Server{}
	for _, path := range []string{"/v1/images/plan", "/v1/images/clean"} {
		request := httptest.NewRequest("GET", "http://updater.local"+path, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != 404 {
			t.Fatalf("service handler exposed %s: %d", path, response.Code)
		}
	}
}
