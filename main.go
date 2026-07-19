package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"
)

// Embed frontend files
//
//go:embed web/*
var webFiles embed.FS

// RealtimeResponse structure for Frontend API
type RealtimeResponse struct {
	Timestamp   time.Time `json:"ts"`
	CPUPercent  float64   `json:"cpu_percent"`
	CPUCores    int       `json:"cpu_cores"`
	CPUModel    string    `json:"cpu_model"`
	CPUTemp     float64   `json:"cpu_temp"`
	Load1       float64   `json:"load_1"`
	Load5       float64   `json:"load_5"`
	Load15      float64   `json:"load_15"`
	RAMPercent  float64   `json:"ram_percent"`
	RAMUsed     uint64    `json:"ram_used"`
	RAMTotal    uint64    `json:"ram_total"`
	SwapPercent float64   `json:"swap_percent"`
	SwapUsed    uint64    `json:"swap_used"`
	SwapTotal   uint64    `json:"swap_total"`
	DiskPercent float64   `json:"disk_percent"`
	DiskUsed    uint64    `json:"disk_used"`
	DiskTotal   uint64    `json:"disk_total"`
	OS          string    `json:"os"`
	HostName    string    `json:"host_name"`
	Uptime      uint64    `json:"uptime"`
	Processes   uint64    `json:"processes"`
}

var (
	lastMetrics RealtimeResponse
	mu          sync.RWMutex
	db          *sql.DB
)

const maxLogBodyBytes = 16 << 10

var logSourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/ -]{0,63}$`)

func main() {
	// Database Path configuration
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "metrics.db"
	}

	var err error
	db, err = sql.Open("duckdb", dbPath)
	if err != nil {
		log.Fatal("failed to open database: ", err)
	}

	// Create tables if not exists
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS metrics (
			ts TIMESTAMPTZ,
			cpu_percent DOUBLE,
			ram_percent DOUBLE,
			ram_used UBIGINT,
			ram_total UBIGINT,
			disk_percent DOUBLE,
			disk_used UBIGINT,
			disk_total UBIGINT,
			source VARCHAR,
			cpu_temp DOUBLE,
			load_1 DOUBLE,
			load_5 DOUBLE,
			load_15 DOUBLE,
			processes UBIGINT,
			swap_percent DOUBLE,
			swap_used UBIGINT,
			swap_total UBIGINT
		)
	`)
	if err != nil {
		log.Fatal("failed to create metrics table: ", err)
	}

	// Migrations: Alter table metrics to include newer columns dynamically
	migrations := []string{
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS cpu_temp DOUBLE`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS load_1 DOUBLE`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS load_5 DOUBLE`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS load_15 DOUBLE`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS processes UBIGINT`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS swap_percent DOUBLE`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS swap_used UBIGINT`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS swap_total UBIGINT`,
	}
	for _, m := range migrations {
		_, _ = db.Exec(m)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS logs (
			ts TIMESTAMPTZ,
			level VARCHAR,
			message VARCHAR,
			source VARCHAR
		)
	`)
	if err != nil {
		log.Fatal("failed to create logs table: ", err)
	}

	// Host details for primary source name
	sourceName := "sentinel"
	hostStat, err := host.Info()
	if err == nil {
		sourceName = hostStat.Hostname
	}

	insertLog("INFO", "Sentinel system monitor starting up...", "system")

	// Start metric collection in background
	go startCollector(sourceName)

	// HTTP Routing Setup
	// 1. Healthcheck Endpoint (Unauthenticated)
	http.HandleFunc("/healthz", handleHealthz)

	// 2. API Endpoints (Basic Auth Protected)
	http.HandleFunc("/api/metrics/realtime", basicAuth(handleRealtimeMetrics))
	http.HandleFunc("/api/metrics/history", basicAuth(handleHistoryMetrics))
	http.HandleFunc("/api/logs", basicAuth(handleLogs))
	http.HandleFunc("/api/system/details", basicAuth(handleSystemDetails))

	// 3. Embedded Static Assets (Basic Auth Protected)
	subFS, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal("failed to extract embedded web files sub-fs: ", err)
	}
	http.Handle("/", basicAuthHandler(http.FileServer(http.FS(subFS))))

	// Configure HTTP Port
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	// Configure HTTP Server with Request Logging middleware
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           securityHeaders(requestLogger(http.DefaultServeMux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// Setup Graceful Shutdown channel listening for OS signals
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	// Launch HTTP Server in background goroutine
	go func() {
		insertLog("INFO", fmt.Sprintf("Web dashboard and API listening on port %s", port), "system")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP listen error: %v", err)
		}
	}()

	// Block until SIGINT or SIGTERM is received
	<-stopChan
	insertLog("INFO", "Shutting down gracefully...", "system")

	// Context with 10-second timeout for server cleanup
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()

	// Shutdown HTTP Server
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	// Close DuckDB Connection safely
	if db != nil {
		insertLog("INFO", "Closing DuckDB database connection...", "system")
		if err := db.Close(); err != nil {
			log.Printf("Failed to close DuckDB connection: %v", err)
		} else {
			log.Println("DuckDB database connection closed cleanly.")
		}
	}
}

// Write a log entry to DuckDB & Standard Output
func insertLog(level, message, source string) {
	consoleMessage := strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(message)
	log.Printf("[%s] [%s] %s\n", level, source, consoleMessage)

	if db == nil {
		return
	}

	_, err := db.Exec(`INSERT INTO logs (ts, level, message, source) VALUES (?, ?, ?, ?)`,
		time.Now(), level, message, source)
	if err != nil {
		fmt.Printf("failed to write log to DuckDB: %v\n", err)
	}
}

// --- Middlewares ---

func credentialsMatch(provided, expected string) bool {
	providedHash := sha256.Sum256([]byte(provided))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) == 1
}

// Basic Auth middleware for HandlerFunc
func basicAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := os.Getenv("AUTH_USER")
		pass := os.Getenv("AUTH_PASSWORD")
		if user == "" || pass == "" {
			next(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || !credentialsMatch(u, user) || !credentialsMatch(p, pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="Sentinel System"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// Basic Auth middleware for general http.Handler (used for static file server)
func basicAuthHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := os.Getenv("AUTH_USER")
		pass := os.Getenv("AUTH_PASSWORD")
		if user == "" || pass == "" {
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || !credentialsMatch(u, user) || !credentialsMatch(p, pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="Sentinel System"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// Request Logger middleware logging request method, URI, remote address, and duration
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bypass logger for healthz endpoint to keep docker logs clean
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("[%s] %s %s - %v\n", r.Method, r.RequestURI, r.RemoteAddr, time.Since(start))
	})
}

// --- Handlers ---

// Handler: GET /healthz
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if db == nil {
		http.Error(w, "database connection is uninitialized", http.StatusInternalServerError)
		return
	}
	var liveness int
	err := db.QueryRow(`SELECT 1`).Scan(&liveness)
	if err != nil || liveness != 1 {
		http.Error(w, fmt.Sprintf("database health check failed: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// Handler: GET /api/metrics/realtime
func handleRealtimeMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	mu.RLock()
	defer mu.RUnlock()
	if err := json.NewEncoder(w).Encode(lastMetrics); err != nil {
		log.Printf("Failed to encode metrics: %v", err)
	}
}

// Handler: GET /api/metrics/history
func handleHistoryMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	timeRange := r.URL.Query().Get("range")

	var queryStr string
	if timeRange == "24h" {
		// Aggregate by 5 minutes to keep chart performance optimal
		queryStr = `
			SELECT time_bucket(INTERVAL '5 MINUTE', ts) as bucket_ts, 
			       AVG(cpu_percent) as cpu, 
			       AVG(ram_percent) as ram 
			FROM metrics 
			WHERE ts > now() - INTERVAL 24 HOUR
			GROUP BY bucket_ts 
			ORDER BY bucket_ts ASC
		`
	} else {
		// High resolution raw metrics for last 1 hour
		queryStr = `
			SELECT ts, cpu_percent, ram_percent 
			FROM metrics 
			WHERE ts > now() - INTERVAL 1 HOUR 
			ORDER BY ts ASC
		`
	}

	rows, err := db.Query(queryStr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() {
		_ = rows.Close()
	}()

	type HistPoint struct {
		Timestamp  time.Time `json:"ts"`
		CPUPercent float64   `json:"cpu_percent"`
		RAMPercent float64   `json:"ram_percent"`
	}

	points := []HistPoint{}
	for rows.Next() {
		var ts time.Time
		var cpuVal, ramVal float64
		if err := rows.Scan(&ts, &cpuVal, &ramVal); err == nil {
			points = append(points, HistPoint{Timestamp: ts, CPUPercent: cpuVal, RAMPercent: ramVal})
		}
	}

	if err := json.NewEncoder(w).Encode(points); err != nil {
		log.Printf("Failed to encode history points: %v", err)
	}
}

// Handler: GET, POST, DELETE /api/logs
func handleLogs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		level := r.URL.Query().Get("level")
		if level == "" {
			level = "ALL"
		}
		query := r.URL.Query().Get("query")
		queryPattern := "%" + strings.ToLower(query) + "%"

		rows, err := db.Query(`
			SELECT ts, level, message, source 
			FROM logs 
			WHERE (level = ? OR ? = 'ALL') AND (lower(message) LIKE ? OR ? = '%%')
			ORDER BY ts DESC 
			LIMIT 100
		`, level, level, queryPattern, queryPattern)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() {
			_ = rows.Close()
		}()

		type LogEntry struct {
			Timestamp time.Time `json:"ts"`
			Level     string    `json:"level"`
			Message   string    `json:"message"`
			Source    string    `json:"source"`
		}

		logList := []LogEntry{}
		for rows.Next() {
			var ts time.Time
			var lvl, msg, src string
			if err := rows.Scan(&ts, &lvl, &msg, &src); err == nil {
				logList = append(logList, LogEntry{Timestamp: ts, Level: lvl, Message: msg, Source: src})
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(logList); err != nil {
			log.Printf("Failed to encode log list: %v", err)
		}

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxLogBodyBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		var entry logInput
		if err := decoder.Decode(&entry); err != nil {
			http.Error(w, "invalid log payload", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			http.Error(w, "request body must contain a single JSON object", http.StatusBadRequest)
			return
		}

		entry, err := normalizeLogInput(entry)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		insertLog(entry.Level, entry.Message, entry.Source)
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		_, err := db.Exec(`DELETE FROM logs`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		insertLog("INFO", "Logs database cleared by client request", "system")
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type logInput struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Source  string `json:"source"`
}

func normalizeLogInput(entry logInput) (logInput, error) {
	entry.Level = strings.ToUpper(strings.TrimSpace(entry.Level))
	entry.Message = strings.TrimSpace(entry.Message)
	entry.Source = strings.TrimSpace(entry.Source)

	if entry.Level == "" {
		entry.Level = "INFO"
	}
	switch entry.Level {
	case "INFO", "WARN", "ERROR":
	default:
		return logInput{}, fmt.Errorf("level must be INFO, WARN, or ERROR")
	}

	if entry.Message == "" {
		return logInput{}, fmt.Errorf("message is required")
	}
	if utf8.RuneCountInString(entry.Message) > 4096 {
		return logInput{}, fmt.Errorf("message must be at most 4096 characters")
	}

	if entry.Source == "" {
		entry.Source = "external"
	}
	if !logSourcePattern.MatchString(entry.Source) {
		return logInput{}, fmt.Errorf("source must be 1-64 characters using letters, numbers, spaces, '.', '_', ':', '/', or '-'")
	}

	return entry, nil
}

// --- Background Collector ---

// Background metrics collection loop
func startCollector(defaultSource string) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	// Cache CPU info
	cpuModel := "Unknown CPU"
	cpuInfos, err := cpu.Info()
	if err == nil && len(cpuInfos) > 0 {
		cpuModel = cpuInfos[0].ModelName
	}
	cpuCores, _ := cpu.Counts(true)

	// Cache OS Platform details
	osName := "Unknown OS"
	hStat, err := host.Info()
	if err == nil {
		osName = fmt.Sprintf("%s %s", hStat.OS, hStat.Platform)
	}
	osName = getHostOS(osName)

	// First Run Metrics Extraction
	collectAndStore(defaultSource, cpuCores, cpuModel, osName)

	for range ticker.C {
		collectAndStore(defaultSource, cpuCores, cpuModel, osName)
	}
}

// Read the actual OS of the host system rather than the Docker container OS
func getHostOS(containerOS string) string {
	hostRoot := os.Getenv("HOST_ROOT")
	if hostRoot == "" {
		return containerOS
	}
	// Try standard paths on the host file system
	paths := []string{
		hostRoot + "/etc/os-release",
		hostRoot + "/usr/lib/os-release",
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "PRETTY_NAME=") {
					val := strings.TrimPrefix(line, "PRETTY_NAME=")
					val = strings.Trim(val, `"`+`'`)
					return val
				}
			}
			for _, line := range lines {
				if strings.HasPrefix(line, "NAME=") {
					val := strings.TrimPrefix(line, "NAME=")
					val = strings.Trim(val, `"`+`'`)
					return val
				}
			}
		}
	}
	return containerOS
}

// Read CPU temperature directly from sysfs (bulletproof on Dockerized ARM/Linux servers)
func readCPUTemperature() float64 {
	paths := []string{
		"/sys/class/thermal",
		"/host/sys/class/thermal",
	}

	hostSys := os.Getenv("HOST_SYS")
	if hostSys != "" {
		paths = append([]string{hostSys + "/class/thermal"}, paths...)
	}

	for _, basePath := range paths {
		files, err := os.ReadDir(basePath)
		if err != nil {
			continue
		}

		for _, file := range files {
			if strings.HasPrefix(file.Name(), "thermal_zone") {
				tempPath := fmt.Sprintf("%s/%s/temp", basePath, file.Name())
				data, err := os.ReadFile(tempPath)
				if err == nil {
					tempStr := strings.TrimSpace(string(data))
					var millidegrees float64
					if _, err := fmt.Sscanf(tempStr, "%f", &millidegrees); err == nil {
						val := millidegrees / 1000.0
						if val > 0 && val < 120 {
							return val
						}
					}
				}
			}
		}
	}

	// Fallback to hwmon paths
	hwmonPaths := []string{
		"/sys/class/hwmon",
		"/host/sys/class/hwmon",
	}
	if hostSys != "" {
		hwmonPaths = append([]string{hostSys + "/class/hwmon"}, hwmonPaths...)
	}

	for _, basePath := range hwmonPaths {
		files, err := os.ReadDir(basePath)
		if err != nil {
			continue
		}
		for _, file := range files {
			hwPath := fmt.Sprintf("%s/%s", basePath, file.Name())
			hwFiles, err := os.ReadDir(hwPath)
			if err != nil {
				continue
			}
			for _, hwFile := range hwFiles {
				if strings.HasPrefix(hwFile.Name(), "temp") && strings.HasSuffix(hwFile.Name(), "_input") {
					data, err := os.ReadFile(fmt.Sprintf("%s/%s", hwPath, hwFile.Name()))
					if err == nil {
						tempStr := strings.TrimSpace(string(data))
						var millidegrees float64
						if _, err := fmt.Sscanf(tempStr, "%f", &millidegrees); err == nil {
							val := millidegrees / 1000.0
							if val > 0 && val < 120 {
								return val
							}
						}
					}
				}
			}
		}
	}

	// Fallback to gopsutil sensors read
	temps, err := host.SensorsTemperatures()
	if err == nil {
		for _, t := range temps {
			name := strings.ToLower(t.SensorKey)
			if strings.Contains(name, "cpu") || strings.Contains(name, "core") || strings.Contains(name, "temp") {
				return t.Temperature
			}
		}
		if len(temps) > 0 {
			return temps[0].Temperature
		}
	}

	return 0.0
}

func collectAndStore(defaultSource string, cpuCores int, cpuModel, osName string) {
	cpuPercents, err := cpu.Percent(0, false)
	var cpuPercent float64
	if err == nil && len(cpuPercents) > 0 {
		cpuPercent = cpuPercents[0]
	}

	vmStat, _ := mem.VirtualMemory()
	var ramPercent float64
	var ramUsed, ramTotal uint64
	if vmStat != nil {
		ramPercent = vmStat.UsedPercent
		ramUsed = vmStat.Used
		ramTotal = vmStat.Total
	}

	swapStat, _ := mem.SwapMemory()
	var swapPercent float64
	var swapUsed, swapTotal uint64
	if swapStat != nil {
		swapPercent = swapStat.UsedPercent
		swapUsed = swapStat.Used
		swapTotal = swapStat.Total
	}

	diskPath := os.Getenv("HOST_ROOT")
	if diskPath == "" {
		diskPath = "/"
	}
	diskStat, _ := disk.Usage(diskPath)
	var diskPercent float64
	var diskUsed, diskTotal uint64
	if diskStat != nil {
		diskPercent = diskStat.UsedPercent
		diskUsed = diskStat.Used
		diskTotal = diskStat.Total
	}

	hStat, _ := host.Info()
	var uptime uint64
	var processes uint64
	hostname := defaultSource
	if hStat != nil {
		uptime = hStat.Uptime
		hostname = hStat.Hostname
		processes = hStat.Procs
	}

	loadStat, err := load.Avg()
	var load1, load5, load15 float64
	if err == nil && loadStat != nil {
		load1 = loadStat.Load1
		load5 = loadStat.Load5
		load15 = loadStat.Load15
	}

	cpuTemp := readCPUTemperature()

	now := time.Now()

	// Update cached state
	mu.Lock()
	lastMetrics = RealtimeResponse{
		Timestamp:   now,
		CPUPercent:  cpuPercent,
		CPUCores:    cpuCores,
		CPUModel:    cpuModel,
		CPUTemp:     cpuTemp,
		Load1:       load1,
		Load5:       load5,
		Load15:      load15,
		RAMPercent:  ramPercent,
		RAMUsed:     ramUsed,
		RAMTotal:    ramTotal,
		SwapPercent: swapPercent,
		SwapUsed:    swapUsed,
		SwapTotal:   swapTotal,
		DiskPercent: diskPercent,
		DiskUsed:    diskUsed,
		DiskTotal:   diskTotal,
		OS:          osName,
		HostName:    hostname,
		Uptime:      uptime,
		Processes:   processes,
	}
	mu.Unlock()

	// Persist to DuckDB
	if db != nil {
		_, err = db.Exec(`
			INSERT INTO metrics (ts, cpu_percent, ram_percent, ram_used, ram_total, disk_percent, disk_used, disk_total, source, cpu_temp, load_1, load_5, load_15, processes, swap_percent, swap_used, swap_total)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, now, cpuPercent, ramPercent, ramUsed, ramTotal, diskPercent, diskUsed, diskTotal, hostname, cpuTemp, load1, load5, load15, processes, swapPercent, swapUsed, swapTotal)
		if err != nil {
			log.Printf("Collector: DB insert failed: %v", err)
		}

		// Database retention pruning (runs once an hour at minute 0)
		if now.Minute() == 0 && now.Second() < 12 {
			_, err1 := db.Exec(`DELETE FROM metrics WHERE ts < now() - INTERVAL 30 DAY`)
			_, err2 := db.Exec(`DELETE FROM logs WHERE ts < now() - INTERVAL 7 DAY`)
			if err1 != nil || err2 != nil {
				log.Printf("Pruner: database pruning failed: metrics_err=%v, logs_err=%v", err1, err2)
			} else {
				insertLog("INFO", "Historical database pruning completed (retained 30 days metrics, 7 days logs)", "system")
			}
		}

		// Dynamic event logs based on metrics
		if cpuPercent > 85.0 {
			insertLog("WARN", fmt.Sprintf("High CPU usage spike: %.2f%%", cpuPercent), "collector")
		}
		if ramPercent > 85.0 {
			insertLog("WARN", fmt.Sprintf("High RAM usage warning: %.2f%%", ramPercent), "collector")
		}
	}
}

// System details and processes structs
type SystemDetailsResponse struct {
	KernelVersion  string        `json:"kernel_version"`
	RebootRequired bool          `json:"reboot_required"`
	Processes      []ProcessInfo `json:"processes"`
	ListeningPorts []PortInfo    `json:"listening_ports"`
}

type ProcessInfo struct {
	PID     int32   `json:"pid"`
	Name    string  `json:"name"`
	CPU     float64 `json:"cpu"`
	Memory  float64 `json:"memory"`
	Command string  `json:"command"`
}

type PortInfo struct {
	Port uint32 `json:"port"`
	PID  int32  `json:"pid"`
	Name string `json:"name"`
}

// Handler: GET /api/system/details
func handleSystemDetails(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	kernel := "Unknown"
	hStat, err := host.Info()
	if err == nil {
		kernel = hStat.KernelVersion
	}

	resp := SystemDetailsResponse{
		KernelVersion:  kernel,
		RebootRequired: isRebootRequired(),
		Processes:      getProcesses(),
		ListeningPorts: getListeningPorts(),
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("Failed to encode system details: %v", err)
	}
}

func getProcesses() []ProcessInfo {
	pList, err := process.Processes()
	if err != nil {
		return nil
	}
	var procs []ProcessInfo
	for _, p := range pList {
		name, err := p.Name()
		if err != nil {
			continue
		}
		memPct, _ := p.MemoryPercent()
		cpuPct, _ := p.CPUPercent()
		cmd, _ := p.Cmdline()
		if len(cmd) > 120 {
			cmd = cmd[:120] + "..."
		}
		procs = append(procs, ProcessInfo{
			PID:     p.Pid,
			Name:    name,
			CPU:     cpuPct,
			Memory:  float64(memPct),
			Command: cmd,
		})
	}
	// Sort by memory usage descending
	sort.Slice(procs, func(i, j int) bool {
		return procs[i].Memory > procs[j].Memory
	})
	if len(procs) > 15 {
		return procs[:15]
	}
	return procs
}

func getListeningPorts() []PortInfo {
	conns, err := net.Connections("tcp")
	if err != nil {
		return nil
	}
	var ports []PortInfo
	seen := make(map[uint32]bool)
	for _, conn := range conns {
		if conn.Status == "LISTEN" {
			port := conn.Laddr.Port
			if seen[port] {
				continue
			}
			seen[port] = true
			procName := "Unknown"
			if conn.Pid > 0 {
				if p, err := process.NewProcess(conn.Pid); err == nil {
					if name, err := p.Name(); err == nil {
						procName = name
					}
				}
			}
			ports = append(ports, PortInfo{
				Port: port,
				PID:  conn.Pid,
				Name: procName,
			})
		}
	}
	// Sort by port ascending
	sort.Slice(ports, func(i, j int) bool {
		return ports[i].Port < ports[j].Port
	})
	return ports
}

func isRebootRequired() bool {
	hostRoot := os.Getenv("HOST_ROOT")
	if hostRoot == "" {
		hostRoot = ""
	}
	_, err := os.Stat(hostRoot + "/var/run/reboot-required")
	return err == nil
}
