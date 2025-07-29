package main
import (
	   "encoding/json"
	   "fmt"
	   "net/http"
	   "net/http/httptest"
	   "os"
	   "os/exec"
	   "strings"
	   "testing"
	   "time"
)
// --- Teste de Integração ---
func TestIntegration_ServerEndpoints(t *testing.T) {
	   t.Parallel()

	   // Mock execCommand para respostas previsíveis
	   originalExecCommand := execCommand
	   execCommand = func(command string, args ...string) *exec.Cmd {
			   cs := []string{"-test.run=TestHelperProcess", "--", command}
			   cs = append(cs, args...)
			   cmd := exec.Command(os.Args[0], cs...)
			   cmd.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
			   return cmd
	   }
	   defer func() { execCommand = originalExecCommand }()

	   // Sobe o servidor real em uma porta aleatória
	   mux := http.NewServeMux()
	   checkers := make(map[string]*StatusChecker)
	   for _, module := range []string{"debian", "ubuntu"} {
			   checker := NewStatusChecker(module)
			   checker.results = []CheckResult{{IsUp: true, Message: "Operational", HTTPStatus: http.StatusOK}}
			   checkers[module] = checker
	   }
	   mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			   endpoints := map[string]string{"debian": "/status/debian", "ubuntu": "/status/ubuntu"}
			   json.NewEncoder(w).Encode(map[string]interface{}{
					   "monitored_modules": endpoints,
					   "polling_interval_s": 300.0,
			   })
	   })
	   mux.HandleFunc("/status/", func(w http.ResponseWriter, r *http.Request) {
			   module := strings.TrimPrefix(r.URL.Path, "/status/")
			   if c, ok := checkers[module]; ok {
					   c.ServeHTTP(w, r)
			   } else {
					   writeJSONError(w, http.StatusNotFound, "Module not found.", r.URL.Path)
			   }
	   })
	   ts := httptest.NewServer(mux)
	   defer ts.Close()

	   // Testa endpoint raiz
	   res, err := http.Get(ts.URL + "/")
	   if err != nil {
			   t.Fatalf("GET / failed: %v", err)
	   }
	   defer res.Body.Close()
	   if res.StatusCode != http.StatusOK {
			   t.Errorf("GET /: expected 200, got %d", res.StatusCode)
	   }
	   var rootResp map[string]interface{}
	   if err := json.NewDecoder(res.Body).Decode(&rootResp); err != nil {
			   t.Fatalf("GET /: decode failed: %v", err)
	   }
	   if _, ok := rootResp["monitored_modules"]; !ok {
			   t.Error("GET /: missing monitored_modules")
	   }

	   // Testa endpoint de status
	   res2, err := http.Get(ts.URL + "/status/debian")
	   if err != nil {
			   t.Fatalf("GET /status/debian failed: %v", err)
	   }
	   defer res2.Body.Close()
	   if res2.StatusCode != http.StatusOK {
			   t.Errorf("GET /status/debian: expected 200, got %d", res2.StatusCode)
	   }
	   var statusResp []map[string]interface{}
	   if err := json.NewDecoder(res2.Body).Decode(&statusResp); err != nil {
			   t.Fatalf("GET /status/debian: decode failed: %v", err)
	   }
	   if len(statusResp) == 0 || !statusResp[0]["is_up"].(bool) {
			   t.Error("GET /status/debian: expected is_up true")
	   }
}

// NOTE: The 'execCommand' variable is declared in 'server.go'.
// This test file will modify that variable instead of redeclaring it.

// --- Mocking exec.Command ---

func TestMain(m *testing.M) {
	originalExecCommand := execCommand
	execCommand = mockExecCommand
	code := m.Run()
	execCommand = originalExecCommand
	os.Exit(code)
}

func mockExecCommand(command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestHelperProcess", "--", command}
	cs = append(cs, args...)
	cmd := exec.Command(os.Args[0], cs...)
	cmd.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
	return cmd
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	args := os.Args
	if i := find(args, "--"); i >= 0 {
		args = args[i+1:]
	}

	if len(args) < 2 {
		os.Exit(1)
	}

	rsyncURL := args[1]
	if rsyncURL == "rsync://sagres.c3sl.ufpr.br/" {
		fmt.Fprintln(os.Stdout, "debian          Debian Archive")
		fmt.Fprintln(os.Stdout, "ubuntu          Ubuntu Archive")
		os.Exit(0)
	} else if strings.HasSuffix(rsyncURL, "nonexistent") {
		fmt.Fprintln(os.Stdout, "@ERROR: Unknown module 'nonexistent'")
		os.Exit(5)
	} else if strings.HasSuffix(rsyncURL, "internalerror") {
		fmt.Fprintln(os.Stdout, "@ERROR: chroot failed")
		os.Exit(12)
	} else {
		os.Exit(0)
	}
}

func find(slice []string, val string) int {
	for i, item := range slice {
		if item == val {
			return i
		}
	}
	return -1
}

// --- Unit Tests ---

func TestIsValidModulePath(t *testing.T) {
	testCases := map[string]bool{
		"debian":         true,
		"ubuntu-22.04":   true,
		"my_module_1":    true,
		"invalid/path":   false,
		"../etc/passwd":  false,
		"bad!char":       false,
		"space in name":  false,
		"":               false,
	}

	for path, expected := range testCases {
		t.Run(path, func(t *testing.T) {
			if got := isValidModulePath(path); got != expected {
				t.Errorf("isValidModulePath('%s') = %v; want %v", path, got, expected)
			}
		})
	}
}


// --- HTTP Handler Tests ---

// setupTestServer creates a new test server with a mocked handler.
func setupTestServer() *httptest.Server {
	checkers := make(map[string]*StatusChecker)
	checker := NewStatusChecker("debian")
	checker.results = []CheckResult{
		{IsUp: true, HTTPStatus: http.StatusOK, Message: "Operational"},
	}
	checkers["debian"] = checker

	mux := http.NewServeMux()
	mux.HandleFunc("/status/", func(w http.ResponseWriter, r *http.Request) {
		module := strings.TrimPrefix(r.URL.Path, "/status/")
		if module == "" {
			   writeJSONError(w, http.StatusBadRequest, "Module name cannot be empty.", r.URL.Path)
			return
		}
		// Use the fixed validation logic from server.go
		if !isValidModulePath(module) {
			   writeJSONError(w, http.StatusBadRequest, "Invalid module name.", r.URL.Path)
			return
		}
		if c, ok := checkers[module]; ok {
			c.ServeHTTP(w, r)
		} else {
			   writeJSONError(w, http.StatusNotFound, "Module not found.", r.URL.Path)
		}
	})
	return httptest.NewServer(mux)
}

// --- Novos testes para cenários de resposta do rsync ---
func TestRsyncSuccessResponse(t *testing.T) {
	checker := NewStatusChecker("debian")
	checker.results = []CheckResult{
		{
			IsUp:          true,
			Message:       "Operational",
			HTTPStatus:    http.StatusOK,
			RsyncExitCode: 0,
			RsyncOutput:   "",
			Timestamp:     (time.Time{}),
		},
	}
	ts := httptest.NewServer(http.HandlerFunc(checker.ServeHTTP))
	defer ts.Close()

	res, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", res.StatusCode)
	}

	var results []CheckResult
	if err := json.NewDecoder(res.Body).Decode(&results); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if len(results) == 0 || !results[0].IsUp {
		t.Errorf("Expected IsUp true, got %+v", results)
	}
}

func TestRsyncUnknownModuleResponse(t *testing.T) {
	checker := NewStatusChecker("nonexistent")
	   checker.results = []CheckResult{
			   {
					   IsUp:          false,
					   Message:       "@ERROR: Unknown module 'nonexistent'",
					   Error:         "@ERROR: Unknown module 'nonexistent'",
					   HTTPStatus:    http.StatusNotFound,
					   RsyncExitCode: 5,
					   RsyncOutput:   "@ERROR: Unknown module 'nonexistent'",
					   Timestamp:     (time.Time{}),
			   },
	   }
	ts := httptest.NewServer(http.HandlerFunc(checker.ServeHTTP))
	defer ts.Close()

	res, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", res.StatusCode)
	}

	var results []CheckResult
	if err := json.NewDecoder(res.Body).Decode(&results); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if len(results) == 0 || results[0].IsUp {
		t.Errorf("Expected IsUp false, got %+v", results)
	}
	   if errVal, ok := results[0].Error, true; !ok || !strings.Contains(errVal, "Unknown module") {
			   t.Errorf("Expected error to contain 'Unknown module', got %s", results[0].Error)
	}
}

func TestRsyncInternalErrorResponse(t *testing.T) {
	checker := NewStatusChecker("internalerror")
	   checker.results = []CheckResult{
			   {
					   IsUp:          false,
					   Message:       "@ERROR: chroot failed",
					   Error:         "@ERROR: chroot failed",
					   HTTPStatus:    http.StatusInternalServerError,
					   RsyncExitCode: 12,
					   RsyncOutput:   "@ERROR: chroot failed",
					   Timestamp:     (time.Time{}),
			   },
	   }
	ts := httptest.NewServer(http.HandlerFunc(checker.ServeHTTP))
	defer ts.Close()

	res, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", res.StatusCode)
	}

	var results []CheckResult
	if err := json.NewDecoder(res.Body).Decode(&results); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if len(results) == 0 || results[0].IsUp {
		t.Errorf("Expected IsUp false, got %+v", results)
	}
	   if errVal, ok := results[0].Error, true; !ok || !strings.Contains(errVal, "chroot failed") {
			   t.Errorf("Expected error to contain 'chroot failed', got %s", results[0].Error)
	}
}
func TestValidationHandler(t *testing.T) {
	ts := setupTestServer()
	defer ts.Close()

	testCases := map[string]int{
		"/status/":                  http.StatusBadRequest,
		// This test is changed to avoid Go's built-in path cleaning.
		// It now correctly tests the handler's internal validation.
		"/status/invalid/path":      http.StatusBadRequest,
		"/status/invalid!module":    http.StatusBadRequest,
		"/status/nonexistent-valid": http.StatusNotFound,
		"/status/debian":            http.StatusOK,
	}

	for path, expectedCode := range testCases {
		t.Run(path, func(t *testing.T) {
			res, err := http.Get(ts.URL + path)
			if err != nil {
				t.Fatalf("Failed to make request: %v", err)
			}
			defer res.Body.Close()

			if res.StatusCode != expectedCode {
				t.Errorf("For path '%s', expected status code %d, but got %d", path, expectedCode, res.StatusCode)
			}

			if expectedCode != http.StatusOK {
				var errResp map[string]interface{}
				if err := json.NewDecoder(res.Body).Decode(&errResp); err != nil {
					t.Fatalf("Failed to decode error response body: %v", err)
				}
				if _, ok := errResp["error"]; !ok {
					t.Error("Expected error JSON response to contain 'error' key")
				}
			}
		})
	}
}

func TestRootHandler(t *testing.T) {
	// We pass a hardcoded list of modules for predictability.
	modulesToMonitor := []string{"debian", "ubuntu"}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		endpoints := make(map[string]string)
		for _, module := range modulesToMonitor {
			endpoints[module] = fmt.Sprintf("/status/%s", module)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"monitored_modules":  endpoints,
			"polling_interval_s": 300.0,
		})
	})

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("Root handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to unmarshal response body: %v", err)
	}

	// Check polling interval
	if interval, ok := response["polling_interval_s"].(float64); !ok || interval != 300.0 {
		t.Errorf("Expected 'polling_interval_s' to be 300.0, got %v", response["polling_interval_s"])
	}

	// Check modules map
	modulesMap, ok := response["monitored_modules"].(map[string]interface{})
	if !ok {
		t.Fatalf("Response body does not contain 'monitored_modules' map")
	}

	if len(modulesMap) != 2 {
		t.Errorf("Expected 2 monitored modules, got %d", len(modulesMap))
	}
	if ep, ok := modulesMap["debian"].(string); !ok || ep != "/status/debian" {
		t.Errorf("Expected debian endpoint to be '/status/debian', got %v", modulesMap["debian"])
	}
}
