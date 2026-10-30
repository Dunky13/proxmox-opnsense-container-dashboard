package frontend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBuildAssetsAndNavigation(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":     {Data: []byte("<html>dashboard</html>")},
		"assets/app.js":  {Data: []byte("console.log('dashboard')")},
		"assets/app.css": {Data: []byte("body{color:green}")},
	}
	handler, err := New(assets)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, content string
		status                int
	}{
		{"GET", "/", "dashboard", 200},
		{"GET", "/settings", "dashboard", 200},
		{"GET", "/assets/app.js", "console.log", 200},
		{"GET", "/assets/missing.js", "404", 404},
		{"GET", "/assets/", "404", 404},
		{"POST", "/settings", "method not allowed", 405},
		{"HEAD", "/", "", 200},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.content) {
				t.Fatalf("response %d %q", response.Code, response.Body.String())
			}
			if tc.method == http.MethodHead && response.Body.Len() != 0 {
				t.Fatal("HEAD returned a body")
			}
		})
	}
	if _, err := New(fstest.MapFS{}); err == nil {
		t.Fatal("missing frontend build was accepted")
	}
}
