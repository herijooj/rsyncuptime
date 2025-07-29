package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- Configuration ---
const apiBaseURL = "http://localhost:8080"
const refreshInterval = 1 * time.Minute
// historyBarWidth agora é dinâmico, depende do tamanho do terminal

// --- Styles ---
var (
	statusUpStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))  // Green
	statusDownStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // Red
	statusPartialStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // Orange
	helpStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	moduleNameStyle    = lipgloss.NewStyle().Bold(true).Width(20)
	errorMsgStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Italic(true)
)

// --- API Data Structures ---
// MODIFIED: Updated to include the new 'message' field from the API.
type CheckResult struct {
	   IsUp          bool      `json:"is_up"`
	   Message       string    `json:"message"`
	   RsyncExitCode int       `json:"rsync_exit_code,omitempty"`
	   RsyncOutput   string    `json:"rsync_output,omitempty"`
	   Timestamp     time.Time `json:"timestamp"`
}

// --- Bubble Tea Messages ---
type statusUpdateMsg struct {
	statuses map[string][]CheckResult
}
type errMsg struct{ err error }

// --- Bubble Tea Model ---
type model struct {
	   statuses   map[string][]CheckResult
	   err        error
	   quitting   bool
	   ticker     *time.Ticker
	   width      int // largura do terminal
	   refreshing bool // indica se o botão de refresh está ativo
}

func initialModel() model {
	   return model{
			   statuses:   make(map[string][]CheckResult),
			   ticker:     time.NewTicker(refreshInterval),
			   width:      80, // valor padrão inicial
			   refreshing: false,
	   }
}

// --- Bubble Tea Commands ---
func fetchStatuses() tea.Cmd {
	return func() tea.Msg {
		resp, err := http.Get(apiBaseURL + "/")
		if err != nil {
			return errMsg{err}
		}
		defer resp.Body.Close()

		var discoveryResponse struct {
			Modules map[string]string `json:"monitored_modules"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&discoveryResponse); err != nil {
			return errMsg{err}
		}

		statuses := make(map[string][]CheckResult)
		var mu sync.Mutex
		var wg sync.WaitGroup

		for name := range discoveryResponse.Modules {
			wg.Add(1)
			go func(moduleName string) {
				defer wg.Done()
				history, err := fetchModuleHistory(moduleName)
				mu.Lock()
				if err != nil {
					statuses[moduleName] = []CheckResult{{IsUp: false, Message: err.Error()}}
				} else {
					statuses[moduleName] = history
				}
				mu.Unlock()
			}(name)
		}
		wg.Wait()

		return statusUpdateMsg{statuses}
	}
}

func fetchModuleHistory(name string) ([]CheckResult, error) {
	resp, err := http.Get(fmt.Sprintf("%s/status/%s", apiBaseURL, name))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var history []CheckResult
	if err := json.Unmarshal(body, &history); err != nil {
		return nil, fmt.Errorf("bad json from api for %s: %w", name, err)
	}
	return history, nil
}

// Command to wait for the next tick.
func (m model) waitForTick() tea.Cmd {
	return func() tea.Msg {
		<-m.ticker.C
		return fetchStatuses()()
	}
}

// --- Bubble Tea Core ---

func (m model) Init() tea.Cmd {
	return tea.Batch(fetchStatuses(), m.waitForTick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	  switch msg := msg.(type) {
	  case tea.KeyMsg:
			  switch msg.String() {
			  case "ctrl+c", "q":
					  m.quitting = true
					  m.ticker.Stop()
					  return m, tea.Quit
			  case "r":
					  m.refreshing = true
					  return m, tea.Batch(fetchStatuses(), resetRefreshCmd())
			  }
	  case tea.WindowSizeMsg:
			  m.width = msg.Width
			  return m, nil
	  case statusUpdateMsg:
			  m.statuses = msg.statuses
			  m.err = nil
			  return m, m.waitForTick() // Wait for the next tick after a successful update.
	  case errMsg:
			  m.err = msg.err
			  return m, m.waitForTick() // Still wait for the next tick even on error.
	  case refreshDoneMsg:
			  m.refreshing = false
			  return m, nil
	  }
	  return m, nil
}

// Mensagem para resetar o estado de refresh
type refreshDoneMsg struct{}

// Comando para resetar o estado de refresh após 500ms
func resetRefreshCmd() tea.Cmd {
	  return func() tea.Msg {
			  time.Sleep(500 * time.Millisecond)
			  return refreshDoneMsg{}
	  }
}

// MODIFIED: View now shows the specific error message for outages.
func (m model) View() string {
	   if m.quitting {
			   return "Bye!\n"
	   }

	   // Defina a largura mínima e máxima do historyBar
	   // Reservar espaço para nome (20), uptime (17), status (12), margem (3)
	   minBarWidth := 10
	   reserved := 20 + 17 + 12 + 3
	   barWidth := m.width - reserved
	   if barWidth < minBarWidth {
			   barWidth = minBarWidth
	   } else if barWidth > 120 {
			   barWidth = 120
	   }

	   var b strings.Builder
	   b.WriteString("Rsync Server Status (Last 24h)\n")
	   b.WriteString(helpStyle.Render("Oldest →" + strings.Repeat("─", barWidth-4) + "→ Recent"))
	   b.WriteString("\n\n")

	   if len(m.statuses) == 0 {
			   if m.err != nil {
					   return fmt.Sprintf("Error fetching data: %v\n\n%s", m.err, helpStyle.Render("Press 'r' to retry, 'q' to quit."))
			   }
			   return "Fetching statuses...\n"
	   }

	   sortedNames := make([]string, 0, len(m.statuses))
	   for name := range m.statuses {
			   sortedNames = append(sortedNames, name)
	   }
	   sort.Strings(sortedNames)

	   for _, name := range sortedNames {
			   history := m.statuses[name]
			   bar := renderHistoryBar(history, barWidth)
			   latestResult := CheckResult{IsUp: true, Message: "Operational"}
			   if len(history) > 0 {
					   latestResult = history[len(history)-1]
			   }

			   // Cálculo do uptime
			   upCount := 0
			   for _, check := range history {
					   if check.IsUp {
							   upCount++
					   }
			   }
			   var uptimePercent float64
			   if len(history) > 0 {
					   uptimePercent = float64(upCount) / float64(len(history)) * 100.0
			   }

			   var statusText string
			   var errorDetails string
			   if !latestResult.IsUp {
					   statusText = statusDownStyle.Render("Outage")
					   // Simplifica: mostra só código rsync e primeira linha do erro
					   var details string
					   if latestResult.RsyncExitCode != 0 {
							   details += fmt.Sprintf("Código rsync: %d. ", latestResult.RsyncExitCode)
					   }
					   var firstLine string
					   if latestResult.RsyncOutput != "" {
							   firstLine = strings.SplitN(latestResult.RsyncOutput, "\n", 2)[0]
					   } else if latestResult.Message != "" {
							   firstLine = strings.SplitN(latestResult.Message, "\n", 2)[0]
					   }
					   if details != "" || firstLine != "" {
							   errorDetails = errorMsgStyle.Render(" Erro: " + details + firstLine)
					   }
			   } else if strings.Contains(bar, "196") {
					   statusText = statusPartialStyle.Render("Partial Outage")
					   errorDetails = errorMsgStyle.Render(" (Recent recovery)")
			   } else {
					   statusText = statusUpStyle.Render("Operational")
			   }

			   // Exibe o nome, uptime, barra, status e detalhes na mesma linha
			   rawUptime := fmt.Sprintf("%.2f %%", uptimePercent)
			   paddedUptime := fmt.Sprintf("%-10s uptime", rawUptime)
			   uptimeStr := helpStyle.Render(paddedUptime)
			   b.WriteString(fmt.Sprintf("%s %s %s %s%s\n", moduleNameStyle.Render(name), uptimeStr, bar, statusText, errorDetails))
	   }

	   // Estilo do botão de refresh
	   var refreshBtn string
	   if m.refreshing {
			   refreshBtn = lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true).Render("[r] refresh now") // amarelo
	   } else {
			   refreshBtn = helpStyle.Render("[r] refresh now")
	   }

	   // Mostra erro ao lado do botão se existir
	   var errorInline string
	   if m.err != nil {
			   errorInline = errorMsgStyle.Render(fmt.Sprintf("  Erro: %v", m.err))
	   }

	   b.WriteString(refreshBtn + "  " + helpStyle.Render("[q] quit") + errorInline)
	   return b.String()
}

func renderHistoryBar(history []CheckResult, width int) string {
	if len(history) == 0 {
		return strings.Repeat(" ", width)
	}

	var b strings.Builder
	totalChecks := len(history)
	if totalChecks == 0 {
		return strings.Repeat(" ", width)
	}

	// If history is shorter than the bar width, display it directly.
	if totalChecks <= width {
		for _, check := range history {
			if check.IsUp {
				b.WriteString(statusUpStyle.Render("█"))
			} else {
				b.WriteString(statusDownStyle.Render("█"))
			}
		}
		b.WriteString(strings.Repeat(" ", width-totalChecks)) // Pad with space
		return b.String()
	}

	// Otherwise, compress the history into buckets.
	bucketSize := float64(totalChecks) / float64(width)
	for i := 0; i < width; i++ {
		start := int(float64(i) * bucketSize)
		end := int(float64(i+1) * bucketSize)
		if end > totalChecks {
			end = totalChecks
		}
		if start >= end { // Ensure bucket is not empty
			if start > 0 {
				start--
			} else {
				continue // Should not happen with correct logic
			}
		}

		isUp := true
		for _, check := range history[start:end] {
			if !check.IsUp {
				isUp = false
				break
			}
		}

		if isUp {
			b.WriteString(statusUpStyle.Render("█"))
		} else {
			b.WriteString(statusDownStyle.Render("█"))
		}
	}
	return b.String()
}

func main() {
	if _, ok := os.LookupEnv("DEBUG"); ok {
		f, err := tea.LogToFile("tui-debug.log", "debug")
		if err != nil {
			fmt.Println("fatal:", err)
			os.Exit(1)
		}
		defer f.Close()
	}

	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Fatalf("Error running program: %v", err)
	}
}