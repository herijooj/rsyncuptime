package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This variable is used by tests to mock the exec.Command function.
var execCommand = exec.Command

// --- Configuration ---
var (
// rsyncURL is the base URL of the rsync server to monitor.
// Can be overridden by the RSYNC_URL environment variable.
rsyncURL = "rsync://sagres.c3sl.ufpr.br/"

// pollingInterval is how often each module is checked.
// Can be overridden by the POLLING_INTERVAL_SECONDS environment variable.
pollingInterval = 5 * time.Minute

// port for the HTTP server. Can be overridden by the PORT environment variable.
serverPort = "8080"
)

// init runs before main() to load configuration from environment variables.
func init() {
   if url := os.Getenv("RSYNC_URL"); url != "" {
	  rsyncURL = url
	  log.Printf("Using custom rsync URL from environment: %s", rsyncURL)
   }

   if intervalStr := os.Getenv("POLLING_INTERVAL_SECONDS"); intervalStr != "" {
	  if intervalSec, err := strconv.Atoi(intervalStr); err == nil && intervalSec > 0 {
		 pollingInterval = time.Duration(intervalSec) * time.Second
		 log.Printf("Using custom polling interval from environment: %v", pollingInterval)
	  } else {
		 log.Printf("WARN: Invalid POLLING_INTERVAL_SECONDS value '%s'. Using default.", intervalStr)
	  }
   }

   if port := os.Getenv("PORT"); port != "" {
	  serverPort = port
	  log.Printf("Using custom server port from environment: %s", serverPort)
   }
}


// --- Data Structures ---
type CheckResult struct {
IsUp          bool      `json:"is_up"`
Message       string    `json:"message,omitempty"`
Error         string    `json:"error,omitempty"`
HTTPStatus    int       `json:"http_status"`
RsyncExitCode int       `json:"rsync_exit_code,omitempty"`
RsyncOutput   string    `json:"rsync_output,omitempty"`
Timestamp     time.Time `json:"timestamp"`
}

type StatusChecker struct {
	mu         sync.RWMutex
	moduleName string
	path       string
	results    []CheckResult
	maxResults int
}

// --- Core Functions ---
func discoverModules(baseURL string) ([]string, error) {
	cmd := execCommand("rsync", baseURL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("rsync command failed: %w\nOutput: %s", err, string(out))
	}
	var modules []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) > 0 {
			modules = append(modules, parts[0])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading rsync output: %w", err)
	}
	return modules, nil
}

func NewStatusChecker(moduleName string) *StatusChecker {
	maxResults := int(24*time.Hour/pollingInterval)
	if maxResults < 1 {
		maxResults = 1
	}
	return &StatusChecker{
		moduleName: moduleName,
		path:       fmt.Sprintf("/%s/", moduleName),
		results:    make([]CheckResult, 0, maxResults),
		maxResults: maxResults,
	}
}

func (sc *StatusChecker) StartPolling() {
	ticker := time.NewTicker(pollingInterval)
	go func() {
		sc.performCheck() // Run first check immediately.
		for range ticker.C {
			sc.performCheck()
		}
	}()
}

func (sc *StatusChecker) performCheck() {
	url := rsyncURL + sc.moduleName
	cmd := execCommand("rsync", url)
	out, err := cmd.CombinedOutput()

	newResult := CheckResult{Timestamp: time.Now()}
	outputStr := string(out)

   if err == nil {
		   newResult.IsUp = true
		   newResult.Message = "Operational"
		   newResult.Error = ""
		   newResult.HTTPStatus = http.StatusOK
		   newResult.RsyncExitCode = 0
   } else {
		   newResult.IsUp = false
		   newResult.Message = ""
		   newResult.RsyncOutput = strings.TrimSpace(outputStr)

		   var exiterr *exec.ExitError
		   if errors.As(err, &exiterr) {
				   if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
						   newResult.RsyncExitCode = status.ExitStatus()
				   }
		   }

		   // Extrai a primeira linha do erro do rsync para o campo Error
		   firstLine := ""
		   lines := strings.Split(outputStr, "\n")
		   for _, l := range lines {
				   l = strings.TrimSpace(l)
				   if l != "" {
						   firstLine = l
						   break
				   }
		   }
		   if firstLine == "" {
				   firstLine = "Erro desconhecido do rsync"
		   }

		   if strings.Contains(outputStr, "@ERROR: Unknown module") {
				   newResult.HTTPStatus = http.StatusNotFound
				   newResult.Error = firstLine
		   } else {
				   newResult.HTTPStatus = http.StatusInternalServerError
				   newResult.Error = firstLine
		   }
   }

	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.results = append(sc.results, newResult)
	if len(sc.results) > sc.maxResults {
		sc.results = sc.results[1:]
	}
}

func (sc *StatusChecker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sc.mu.RLock()
	resultsCopy := make([]CheckResult, len(sc.results))
	copy(resultsCopy, sc.results)
	sc.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")

	if len(resultsCopy) > 0 {
		latestStatus := resultsCopy[len(resultsCopy)-1].HTTPStatus
		w.WriteHeader(latestStatus)
	}

	// Adiciona o campo 'code' com o valor do RsyncExitCode em erros
	var resp []map[string]interface{}
	for _, res := range resultsCopy {
		m := make(map[string]interface{})
		m["is_up"] = res.IsUp
		m["success"] = res.IsUp
		if res.IsUp {
			m["message"] = res.Message
		} else {
			m["error"] = res.Error
		}
		m["http_status"] = res.HTTPStatus
		m["timestamp"] = res.Timestamp
		m["path"] = sc.path
		if res.RsyncOutput != "" {
			m["rsync_output"] = res.RsyncOutput
		}
		// Se for erro, coloca o code do rsync
		if !res.IsUp {
			m["code"] = res.RsyncExitCode
			if res.RsyncExitCode != 0 {
				m["rsync_exit_code"] = res.RsyncExitCode
			}
		} else {
			m["code"] = 0
		}
		resp = append(resp, m)
	}
	json.NewEncoder(w).Encode(resp)
}

// isValidModulePath checks if the module name contains only allowed characters.
// This prevents path traversal and other injection attacks.
func isValidModulePath(module string) bool {
	// Allows alphanumeric characters, underscore, hyphen, and dot.
	// This is a safe subset for module names.
	matched, _ := regexp.MatchString(`^[a-zA-Z0-9_.-]+$`, module)
	return matched
}

// writeJSONError envia resposta de erro JSON padronizada, incluindo o path.
func writeJSONError(w http.ResponseWriter, statusCode int, message string, path string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"path":    path,
		"success": false,
		"error":   message,
		"code":    statusCode,
	})
}

func main() {
	log.Println("Discovering rsync modules...")
	discoveredModules, err := discoverModules(rsyncURL)
	if err != nil {
		log.Fatalf("FATAL: Could not discover modules to monitor. Exiting. Error: %v", err)
	}
	log.Printf("Discovered %d modules to monitor.", len(discoveredModules))

	// Store all checkers in a map for easy lookup.
	checkers := make(map[string]*StatusChecker)
	for _, module := range discoveredModules {
		checker := NewStatusChecker(module)
		checker.StartPolling()
		checkers[module] = checker
	}

	mux := http.NewServeMux()

	// Handler for the root endpoint, listing available modules.
	   mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			   if r.URL.Path != "/" {
					   writeJSONError(w, http.StatusNotFound, "Endpoint not found. See / for available modules.", r.URL.Path)
					   return
			   }

			   w.Header().Set("Content-Type", "application/json")
			   endpoints := make(map[string]string)
			   for _, module := range discoveredModules {
					   endpoints[module] = fmt.Sprintf("/status/%s", module)
			   }

			   // Tenta obter a lista de diretórios do rsync
			   var rsyncDirs []string
			   out, err := execCommand("rsync", rsyncURL).CombinedOutput()
			   if err == nil {
					   scanner := bufio.NewScanner(strings.NewReader(string(out)))
					   for scanner.Scan() {
							   line := strings.TrimSpace(scanner.Text())
							   if line == "" {
									   continue
							   }
							   // Pega o nome do diretório (primeira palavra)
							   parts := strings.Fields(line)
							   if len(parts) > 0 {
									   rsyncDirs = append(rsyncDirs, parts[0])
							   }
					   }
			   }

			   json.NewEncoder(w).Encode(map[string]interface{}{
					   "path": "/",
					   "success": true,
					   "message":            "Monitoring all discovered modules. See endpoints below.",
					   "monitored_modules":  endpoints,
					   "polling_interval_s": pollingInterval.Seconds(),
					   "rsync_directories":  rsyncDirs,
			   })
	   })

	// A single handler for all /status/ requests that validates input.
	mux.HandleFunc("/status/", func(w http.ResponseWriter, r *http.Request) {
		module := strings.TrimPrefix(r.URL.Path, "/status/")
			   if module == "" {
					   writeJSONError(w, http.StatusBadRequest, "Module name cannot be empty. Path should be /status/<module-name>.", r.URL.Path)
					   return
			   }
			   if !isValidModulePath(module) {
					   writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Nome de módulo inválido: '%s'. Permitidos apenas letras, números, hífen, underline e ponto. Exemplo válido: debian-archive. Consulte a documentação.", module), r.URL.Path)
					   return
			   }

			   checker, found := checkers[module]
			   if !found {
					   writeJSONError(w, http.StatusNotFound, fmt.Sprintf("Module '%s' is not monitored.", module), r.URL.Path)
					   return
			   }
		checker.ServeHTTP(w, r)
	})


   log.Printf("Starting monitoring server on :%s using rsync URL '%s'", serverPort, rsyncURL)
   if err := http.ListenAndServe(":"+serverPort, mux); err != nil {
	  log.Fatalf("Server failed to start: %s", err)
   }
}
