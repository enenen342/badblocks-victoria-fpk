package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Server struct {
	addr       string
	uiDir      string
	badblocks  string
	mu         sync.Mutex
	scans      map[string]*Scan
	lastDiskSet map[string]Disk
}

type Disk struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Model      string `json:"model"`
	Serial     string `json:"serial"`
	Size       uint64 `json:"size"`
	SizeHuman  string `json:"sizeHuman"`
	Type       string `json:"type"`
	Rotational bool   `json:"rotational"`
	Transport  string `json:"transport"`
	State      string `json:"state"`
	Mountpoint string `json:"mountpoint"`
	FSType     string `json:"fsType"`
}

type scanRequest struct {
	Path      string `json:"path"`
	BlockSize int    `json:"blockSize"`
}

type Scan struct {
	ID         string    `json:"id"`
	Path       string    `json:"path"`
	Command    []string  `json:"command"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Progress   float64   `json:"progress"`
	Elapsed    string    `json:"elapsed"`
	ErrRead    int       `json:"errRead"`
	ErrWrite   int       `json:"errWrite"`
	ErrCompare int       `json:"errCompare"`
	BadBlocks  int       `json:"badBlocks"`
	ExitCode   int       `json:"exitCode"`
	Error      string    `json:"error,omitempty"`

	ctx    context.Context
	cancel context.CancelFunc
	lines  []string
	subs   map[chan string]struct{}
	mu     sync.Mutex
}

type lsblkOutput struct {
	BlockDevices []lsblkDevice `json:"blockdevices"`
}

type lsblkDevice struct {
	Name        string        `json:"name"`
	KName       string        `json:"kname"`
	Path        string        `json:"path"`
	Model       string        `json:"model"`
	Serial      string        `json:"serial"`
	Size        json.RawMessage `json:"size"`
	Type        string        `json:"type"`
	Rota        json.RawMessage `json:"rota"`
	Tran        string        `json:"tran"`
	State       string        `json:"state"`
	Mountpoints []string      `json:"mountpoints"`
	Mountpoint  string        `json:"mountpoint"`
	FSType      string        `json:"fstype"`
	Children    []lsblkDevice `json:"children"`
}

var progressRE = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)%`)
var badblocksProgressRE = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)%\s+done`)
var fractionProgressRE = regexp.MustCompile(`(\d+)\s*/\s*(\d+)`)
var badblocksStatsRE = regexp.MustCompile(`([0-9:]+)\s+elapsed.*?\(\s*([0-9]+)\s*/\s*([0-9]+)\s*/\s*([0-9]+)`)
var badBlockRE = regexp.MustCompile(`^\s*([0-9]+)\s*$`)

func main() {
	addr := flag.String("addr", "127.0.0.1:24046", "HTTP listen address")
	uiDir := flag.String("ui", "ui", "static UI directory")
	badblocks := flag.String("badblocks", "badblocks", "badblocks executable")
	flag.Parse()

	srv := &Server{
		addr:       *addr,
		uiDir:      *uiDir,
		badblocks:  *badblocks,
		scans:      make(map[string]*Scan),
		lastDiskSet: make(map[string]Disk),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", srv.handleHealth)
	mux.HandleFunc("/api/disks", srv.handleDisks)
	mux.HandleFunc("/api/scan", srv.handleStartScan)
	mux.HandleFunc("/api/scans/", srv.handleScanByID)
	mux.Handle("/", http.FileServer(http.Dir(*uiDir)))

	log.Printf("fn-badblocks-victoria listening on %s, ui=%s", *addr, *uiDir)
	if err := http.ListenAndServe(*addr, logging(mux)); err != nil {
		log.Fatal(err)
	}
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"badblocksAvailable":  commandExists(s.badblocks),
		"serverTime":          time.Now().Format(time.RFC3339),
		"readonlyScanDefault": true,
	})
}

func (s *Server) handleDisks(w http.ResponseWriter, r *http.Request) {
	disks, err := listDisks()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	set := make(map[string]Disk, len(disks))
	for _, d := range disks {
		set[d.Path] = d
	}
	s.mu.Lock()
	s.lastDiskSet = set
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"disks":               disks,
		"badblocksAvailable":  commandExists(s.badblocks),
		"readonlyScanDefault": true,
	})
}

func (s *Server) handleStartScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req scanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.BlockSize == 0 {
		req.BlockSize = 4096
	}
	if req.BlockSize < 512 || req.BlockSize > 1048576 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "blockSize must be between 512 and 1048576"})
		return
	}
	if err := s.validateDisk(req.Path); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if !commandExists(s.badblocks) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "badblocks command not found"})
		return
	}

	id := randomID()
	ctx, cancel := context.WithCancel(context.Background())
	args := []string{"-sv", "-b", strconv.Itoa(req.BlockSize), req.Path}
	scan := &Scan{
		ID:        id,
		Path:      req.Path,
		Command:   append([]string{s.badblocks}, args...),
		Status:    "running",
		StartedAt: time.Now(),
		ExitCode:  -1,
		ctx:       ctx,
		cancel:    cancel,
		subs:      make(map[chan string]struct{}),
	}

	s.mu.Lock()
	s.scans[id] = scan
	s.mu.Unlock()

	go s.runScan(scan, args)
	writeJSON(w, http.StatusAccepted, scan.snapshot())
}

func (s *Server) handleScanByID(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/scans/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	scan := s.getScan(parts[0])
	if scan == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "scan not found"})
		return
	}
	if len(parts) == 2 && parts[1] == "events" {
		s.handleScanEvents(w, r, scan)
		return
	}
	if len(parts) == 2 && parts[1] == "stop" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		scan.cancel()
		scan.addLine("stop requested")
		writeJSON(w, http.StatusOK, scan.snapshot())
		return
	}
	writeJSON(w, http.StatusOK, scan.snapshot())
}

func (s *Server) handleScanEvents(w http.ResponseWriter, r *http.Request, scan *Scan) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := scan.subscribe()
	defer scan.unsubscribe(ch)

	for _, line := range scan.recentLines() {
		writeEvent(w, "line", line)
	}
	writeEventJSON(w, "state", scan.snapshot())
	flusher.Flush()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-ch:
			writeEvent(w, "line", line)
			writeEventJSON(w, "state", scan.snapshot())
			flusher.Flush()
			if scan.isFinished() {
				return
			}
		case <-ticker.C:
			writeEventJSON(w, "state", scan.snapshot())
			flusher.Flush()
			if scan.isFinished() {
				return
			}
		}
	}
}

func (s *Server) runScan(scan *Scan, args []string) {
	cmd := exec.CommandContext(scan.ctx, s.badblocks, args...)
	scan.Command = append([]string{s.badblocks}, args...)

	stdout, outErr := cmd.StdoutPipe()
	if outErr != nil {
		scan.finish(1, outErr)
		return
	}
	stderr, errErr := cmd.StderrPipe()
	if errErr != nil {
		scan.finish(1, errErr)
		return
	}
	if err := cmd.Start(); err != nil {
		scan.finish(1, err)
		return
	}
	scan.addLine("$ " + strings.Join(scan.Command, " "))

	var wg sync.WaitGroup
	wg.Add(2)
	go streamLines(stdout, scan, &wg)
	go streamLines(stderr, scan, &wg)
	wg.Wait()
	err := cmd.Wait()
	if scan.ctx.Err() == context.Canceled {
		scan.finish(130, errors.New("scan stopped by user"))
		return
	}
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	scan.finish(exitCode, err)
}

func buildCommandLine(args ...string) string {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, shellQuote(a))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n\"'\\") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func streamLines(r io.Reader, scan *Scan, wg *sync.WaitGroup) {
	defer wg.Done()
	br := bufio.NewReader(r)
	var buf bytes.Buffer
	lastProgressLine := ""
	lastProgressEmit := time.Time{}
	emitProgress := func(force bool) {
		if buf.Len() == 0 {
			return
		}
		line := cleanTerminalLine(buf.String())
		if line == "" || (!strings.Contains(line, "%") && !fractionProgressRE.MatchString(line)) {
			return
		}
		now := time.Now()
		if !force {
			if line == lastProgressLine {
				return
			}
			if !lastProgressEmit.IsZero() && now.Sub(lastProgressEmit) < 250*time.Millisecond {
				return
			}
		}
		scan.addLine(line)
		lastProgressLine = line
		lastProgressEmit = now
	}
	for {
		b, err := br.ReadByte()
		if b == '\n' || b == '\r' {
			emitProgress(true)
			if strings.TrimSpace(buf.String()) != "" {
				scan.addLine(cleanTerminalLine(buf.String()))
			}
			buf.Reset()
		} else if err == nil {
			buf.WriteByte(b)
			emitProgress(false)
		}
		if err != nil {
			emitProgress(true)
			if strings.TrimSpace(buf.String()) != "" {
				scan.addLine(cleanTerminalLine(buf.String()))
			}
			return
		}
	}
}

func (s *Server) validateDisk(path string) error {
	if path == "" || !strings.HasPrefix(path, "/dev/") || strings.Contains(path, "..") {
		return errors.New("invalid disk path")
	}
	disks, err := listDisks()
	if err == nil {
		set := make(map[string]Disk, len(disks))
		for _, d := range disks {
			set[d.Path] = d
		}
		s.mu.Lock()
		s.lastDiskSet = set
		s.mu.Unlock()
	}
	s.mu.Lock()
	_, ok := s.lastDiskSet[path]
	s.mu.Unlock()
	if !ok {
		return errors.New("disk is not in the detected disk list")
	}
	return nil
}

func (s *Server) getScan(id string) *Scan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scans[id]
}

func (scan *Scan) addLine(line string) {
	line = strings.TrimSpace(cleanTerminalLine(line))
	if line == "" {
		return
	}
	progress := -1.0
	if m := badblocksProgressRE.FindStringSubmatch(line); len(m) == 2 {
		if p, err := strconv.ParseFloat(m[1], 64); err == nil {
			progress = p
		}
	} else if m := progressRE.FindStringSubmatch(line); len(m) == 2 {
		if p, err := strconv.ParseFloat(m[1], 64); err == nil {
			progress = p
		}
	} else if m := fractionProgressRE.FindStringSubmatch(line); len(m) == 3 {
		current, err1 := strconv.ParseFloat(m[1], 64)
		total, err2 := strconv.ParseFloat(m[2], 64)
		if err1 == nil && err2 == nil && total > 0 {
			progress = (current / total) * 100
		}
	}
	isBadBlock := badBlockRE.MatchString(line)

	var elapsed string
	var errRead, errWrite, errCompare int
	hasStats := false
	if m := badblocksStatsRE.FindStringSubmatch(line); len(m) == 5 {
		hasStats = true
		elapsed = m[1]
		errRead, _ = strconv.Atoi(m[2])
		errWrite, _ = strconv.Atoi(m[3])
		errCompare, _ = strconv.Atoi(m[4])
	}

	scan.mu.Lock()
	if hasStats {
		scan.Elapsed = elapsed
		scan.ErrRead = errRead
		scan.ErrWrite = errWrite
		scan.ErrCompare = errCompare
	}
	if progress >= 0 && progress >= scan.Progress {
		scan.Progress = progress
	}
	if isBadBlock {
		scan.BadBlocks++
	}
	scan.lines = append(scan.lines, time.Now().Format("15:04:05")+"  "+line)
	if len(scan.lines) > 1000 {
		scan.lines = scan.lines[len(scan.lines)-1000:]
	}
	for ch := range scan.subs {
		select {
		case ch <- scan.lines[len(scan.lines)-1]:
		default:
		}
	}
	scan.mu.Unlock()
}

func cleanTerminalLine(line string) string {
	var out []rune
	for _, r := range line {
		switch {
		case r == '\b' || r == 0x7f:
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		case r == '\t':
			out = append(out, ' ')
		case r == '\uFFFD':
			continue
		case r < 32:
			continue
		default:
			out = append(out, r)
		}
	}
	cleaned := strings.Join(strings.Fields(string(out)), " ")
	if idx := strings.LastIndex(cleaned, "Checking for bad blocks"); idx > 0 {
		cleaned = cleaned[idx:]
	}
	return cleaned
}

func (scan *Scan) finish(exitCode int, err error) {
	scan.mu.Lock()
	scan.ExitCode = exitCode
	scan.FinishedAt = time.Now()
	if err != nil {
		scan.Error = err.Error()
	}
	if exitCode == 0 {
		scan.Status = "finished"
		scan.Progress = 100
	} else if exitCode == 130 {
		scan.Status = "stopped"
	} else {
		scan.Status = "failed"
	}
	scan.mu.Unlock()
	scan.addLine(fmt.Sprintf("scan %s, exit_code=%d", scan.Status, exitCode))
}

func (scan *Scan) subscribe() chan string {
	ch := make(chan string, 64)
	scan.mu.Lock()
	scan.subs[ch] = struct{}{}
	scan.mu.Unlock()
	return ch
}

func (scan *Scan) unsubscribe(ch chan string) {
	scan.mu.Lock()
	delete(scan.subs, ch)
	close(ch)
	scan.mu.Unlock()
}

func (scan *Scan) recentLines() []string {
	scan.mu.Lock()
	defer scan.mu.Unlock()
	cp := append([]string(nil), scan.lines...)
	return cp
}

func (scan *Scan) isFinished() bool {
	scan.mu.Lock()
	defer scan.mu.Unlock()
	return scan.Status != "running"
}

func (scan *Scan) snapshot() Scan {
	scan.mu.Lock()
	defer scan.mu.Unlock()
	cp := *scan
	formatDuration := func(d time.Duration) string {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		s := int(d.Seconds()) % 60
		if h > 0 {
			return fmt.Sprintf("%d:%02d:%02d", h, m, s)
		}
		return fmt.Sprintf("%d:%02d", m, s)
	}

	if cp.Status == "running" {
		cp.Elapsed = formatDuration(time.Since(cp.StartedAt).Round(time.Second))
	} else if !cp.FinishedAt.IsZero() {
		cp.Elapsed = formatDuration(cp.FinishedAt.Sub(cp.StartedAt).Round(time.Second))
	}
	cp.ctx = nil
	cp.cancel = nil
	cp.subs = nil
	cp.lines = nil
	return cp
}

func listDisks() ([]Disk, error) {
	out, err := exec.Command("lsblk", "-J", "-b", "-o", "NAME,KNAME,PATH,MODEL,SERIAL,SIZE,TYPE,ROTA,TRAN,STATE,MOUNTPOINTS,FSTYPE").Output()
	if err != nil {
		return listDisksFromSys()
	}
	var parsed lsblkOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}
	var disks []Disk
	var walk func([]lsblkDevice)
	walk = func(devs []lsblkDevice) {
		for _, d := range devs {
			if d.Type == "disk" && isPhysicalDiskName(firstNonEmpty(d.KName, d.Name, filepath.Base(d.Path))) {
				disks = append(disks, convertDisk(d))
			}
			if len(d.Children) > 0 {
				walk(d.Children)
			}
		}
	}
	walk(parsed.BlockDevices)
	return disks, nil
}

func convertDisk(d lsblkDevice) Disk {
	path := d.Path
	if path == "" {
		path = "/dev/" + firstNonEmpty(d.KName, d.Name)
	}
	size := parseJSONUint(d.Size)
	rot := parseJSONBool(d.Rota)
	mounts := d.Mountpoints
	if len(mounts) == 0 && d.Mountpoint != "" {
		mounts = []string{d.Mountpoint}
	}
	return Disk{
		Name:       firstNonEmpty(d.Name, d.KName, filepath.Base(path)),
		Path:       path,
		Model:      strings.TrimSpace(d.Model),
		Serial:     strings.TrimSpace(d.Serial),
		Size:       size,
		SizeHuman:  humanBytes(size),
		Type:       d.Type,
		Rotational: rot,
		Transport:  d.Tran,
		State:      d.State,
		Mountpoint: strings.Join(compact(mounts), ", "),
		FSType:     d.FSType,
	}
}

func listDisksFromSys() ([]Disk, error) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}
	var disks []Disk
	for _, e := range entries {
		name := e.Name()
		if !isPhysicalDiskName(name) {
			continue
		}
		sizeBytes := readUint(filepath.Join("/sys/block", name, "size")) * 512
		rot := readUint(filepath.Join("/sys/block", name, "queue/rotational")) == 1
		model := readString(filepath.Join("/sys/block", name, "device/model"))
		serial := readString(filepath.Join("/sys/block", name, "device/serial"))
		disks = append(disks, Disk{
			Name:       name,
			Path:       "/dev/" + name,
			Model:      model,
			Serial:     serial,
			Size:       sizeBytes,
			SizeHuman:  humanBytes(sizeBytes),
			Type:       "disk",
			Rotational: rot,
		})
	}
	return disks, nil
}

func readUint(path string) uint64 {
	s := strings.TrimSpace(readString(path))
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

func parseJSONUint(raw json.RawMessage) uint64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var n uint64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil && f > 0 {
		return uint64(f)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
		return n
	}
	return 0
}

func parseJSONBool(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	var n uint64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n != 0
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.ToLower(strings.TrimSpace(s))
		return s == "1" || s == "true" || s == "yes"
	}
	return false
}

func readString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func isPhysicalDiskName(name string) bool {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "" {
		return false
	}
	if strings.HasPrefix(name, "loop") ||
		strings.HasPrefix(name, "ram") ||
		strings.HasPrefix(name, "zram") ||
		strings.HasPrefix(name, "dm-") ||
		strings.HasPrefix(name, "md") {
		return false
	}
	return strings.HasPrefix(name, "sd") ||
		strings.HasPrefix(name, "hd") ||
		strings.HasPrefix(name, "vd") ||
		strings.HasPrefix(name, "xvd") ||
		strings.HasPrefix(name, "nvme") ||
		strings.HasPrefix(name, "mmcblk")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeEvent(w io.Writer, name, data string) {
	_, _ = fmt.Fprintf(w, "event: %s\n", name)
	for _, line := range strings.Split(data, "\n") {
		_, _ = fmt.Fprintf(w, "data: %s\n", line)
	}
	_, _ = fmt.Fprint(w, "\n")
}

func writeEventJSON(w io.Writer, name string, v any) {
	b, _ := json.Marshal(v)
	writeEvent(w, name, string(b))
}

func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func humanBytes(n uint64) string {
	if n == 0 {
		return "0 B"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

func compact(v []string) []string {
	out := make([]string, 0, len(v))
	for _, s := range v {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
