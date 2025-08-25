package cmd

import (
	"container/list"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

type MirrorContext struct {
	BaseURL       *url.URL
	OutputDir     string
	VisitedURLs   sync.Map
	RejectList    []string
	ExcludeDirs   []string
	ConvertLinks  bool
	MaxDepth      int
	Client        *http.Client
	mu            sync.Mutex
	waitGroup     sync.WaitGroup
	workQueue     *list.List
	activeWorkers int
	maxWorkers    int
	stats         *MirrorStats
	baseURL       *url.URL
}

type MirrorStats struct {
	TotalDiscovered int
	TotalDownloaded int
	TotalFailed     int
	TotalSkipped    int
	StartTime       time.Time
}
type MirrorDownloadTask struct {
	URL   string
	Depth int
}

func InitMirroring(startURL string) error {
	parsedURL, err := url.Parse(startURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	outputDir := parsedURL.Host
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	var rejectList []string
	if MirrorFlagsConfig.Reject != "" {
		rejectList = strings.Split(MirrorFlagsConfig.Reject, ",")
		for i, ext := range rejectList {
			ext = strings.TrimSpace(ext)
			if ext != "" {
				if !strings.HasPrefix(ext, ".") {
					ext = "." + ext
				}
				rejectList[i] = strings.ToLower(ext)
			}
		}
	}

	var excludeDirs []string
	if MirrorFlagsConfig.Exclude != "" {
		excludeDirs = strings.Split(MirrorFlagsConfig.Exclude, ",")
		for i, dir := range excludeDirs {
			dir = strings.TrimSpace(dir)
			dir = strings.Trim(dir, "/")
			if dir != "" {
				excludeDirs[i] = dir
			}
		}
	}

	ctx := &MirrorContext{
		BaseURL:      parsedURL,
		OutputDir:    outputDir,
		RejectList:   rejectList,
		ExcludeDirs:  excludeDirs,
		ConvertLinks: MirrorFlagsConfig.ConvertLinks,
		MaxDepth:     MirrorFlagsConfig.Depth,
		workQueue:    list.New(),
		maxWorkers:   5,
		stats: &MirrorStats{
			StartTime: time.Now(),
		},
		baseURL: nil,
		Client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}

	fmt.Printf("Mirroring website: %s\n", startURL)
	fmt.Printf("Output directory: %s/\n", outputDir)
	fmt.Printf("Max depth: %d\n", ctx.MaxDepth)

	if len(rejectList) > 0 {
		fmt.Printf("Rejecting extensions: %s\n", strings.Join(rejectList, ", "))
	}
	if len(excludeDirs) > 0 {
		fmt.Printf("Excluding directories: %s\n", strings.Join(excludeDirs, ", "))
	}
	if ctx.ConvertLinks {
		fmt.Println("Converting links for offline viewing")
	}
	fmt.Println()

	ctx.enqueueTask(MirrorDownloadTask{
		URL:   startURL,
		Depth: 0,
	})

	ctx.startWorkers()

	ctx.waitGroup.Wait()

	ctx.printStatistics()

	fmt.Printf("\nMirroring completed! Saved to %s/\n", outputDir)
	return nil
}

func (ctx *MirrorContext) enqueueTask(task MirrorDownloadTask) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	if task.Depth > ctx.MaxDepth {
		ctx.stats.TotalSkipped++
		return
	}

	if _, visited := ctx.VisitedURLs.Load(task.URL); visited {
		ctx.stats.TotalSkipped++
		return
	}

	ctx.VisitedURLs.Store(task.URL, true) // Mark as visited before adding to queue
	ctx.workQueue.PushBack(task)
	ctx.stats.TotalDiscovered++

	// Signal a worker if one is idle, or start a new one if below max
	if ctx.activeWorkers < ctx.maxWorkers && ctx.workQueue.Len() == 1 { // Only signal if a new task arrived and workers might be idle
		// No explicit signal needed with this worker pool pattern, workers will pick up from queue
	}
}

func (ctx *MirrorContext) dequeueTask() (MirrorDownloadTask, bool) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	if ctx.workQueue.Len() == 0 {
		return MirrorDownloadTask{}, false
	}

	front := ctx.workQueue.Front()
	task := front.Value.(MirrorDownloadTask)
	ctx.workQueue.Remove(front)
	return task, true
}

func (ctx *MirrorContext) startWorkers() {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	for i := 0; i < ctx.maxWorkers; i++ { // Start all workers initially
		ctx.waitGroup.Add(1)       // Increment for each worker
		ctx.activeWorkers++        // Track active workers
		go ctx.mirrorWorker(i + 1) // Start the worker goroutine
	}
}

func (ctx *MirrorContext) isWorkComplete() bool {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	return ctx.workQueue.Len() == 0 && ctx.activeWorkers <= 1
}

func (ctx *MirrorContext) mirrorWorker(workerID int) {
	defer func() {
		ctx.mu.Lock()
		ctx.activeWorkers--
		ctx.mu.Unlock()
		ctx.waitGroup.Done()
	}()

	for {
		task, ok := ctx.dequeueTask()
		if !ok {
			if ctx.isWorkComplete() {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		ctx.processDownloadTask(task, workerID)
	}
}

func (ctx *MirrorContext) printStatistics() {
	elapsed := time.Since(ctx.stats.StartTime)

	fmt.Printf("\n=== Mirroring Statistics ===\n")
	fmt.Printf("Total URLs discovered: %d\n", ctx.stats.TotalDiscovered)
	fmt.Printf("Successfully downloaded: %d\n", ctx.stats.TotalDownloaded)
	fmt.Printf("Failed downloads: %d\n", ctx.stats.TotalFailed)
	fmt.Printf("Skipped URLs: %d\n", ctx.stats.TotalSkipped)
	fmt.Printf("Time elapsed: %v\n", elapsed.Round(time.Second))
}

func (ctx *MirrorContext) processDownloadTask(task MirrorDownloadTask, workerID int) {
	// Check depth again, though it should be handled by enqueueTask
	if task.Depth > ctx.MaxDepth {
		ctx.stats.TotalSkipped++
		return
	}

	// Check visited again, though it should be handled by enqueueTask
	if _, visited := ctx.VisitedURLs.Load(task.URL); visited {
		ctx.stats.TotalSkipped++
		return
	}
	ctx.VisitedURLs.Store(task.URL, true)

	filePath, err := ctx.downloadResource(task.URL)
	if err != nil {
		log.Printf("Failed to download %s: %v", task.URL, err)
		return
	}

	if ctx.isHTMLFile(task.URL) || strings.HasSuffix(filePath, ".html") {
		fmt.Printf("[Worker %d] Depth %d: %s (Downloaded HTML)\n", workerID, task.Depth, task.URL)
		ctx.parseHTMLForLinks(filePath, task.URL, task.Depth+1)
	} else {
		fmt.Printf("[Worker %d] Depth %d: %s (Downloaded resource)\n", workerID, task.Depth, task.URL)
	}
}

func (ctx *MirrorContext) downloadResource(urlStr string) (string, error) {
	if ctx.shouldReject(urlStr) {
		return "", fmt.Errorf("rejected by extension filter")
	}

	if ctx.isExcludedDirectory(urlStr) {
		return "", fmt.Errorf("rejected by directory filter")
	}

	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Wget/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := ctx.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}

	contentType := resp.Header.Get("Content-Type")
	isHTML := strings.Contains(contentType, "text/html") || ctx.isHTMLFile(urlStr)

	filePath, err := ctx.getLocalPath(urlStr, isHTML)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return "", err
	}

	file, err := os.Create(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return "", err
	}

	if isHTML && ctx.ConvertLinks {
		if err := ctx.convertLinksInHTML(filePath, urlStr); err != nil {
			log.Printf("Link conversion failed for %s: %v", filePath, err)
		}
	}

	return filePath, nil
}

func (ctx *MirrorContext) getLocalPath(urlStr string, isHTML bool) (string, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}

	path := parsedURL.Path
	if path == "" {
		path = "/index.html"
	} else if strings.HasSuffix(path, "/") {
		path += "index.html"
	} else if isHTML && !strings.Contains(path, ".") {
		path += ".html"
	}

	localPath := filepath.Join(ctx.OutputDir, path)
	localPath = filepath.Clean(localPath)

	return localPath, nil
}

func (ctx *MirrorContext) isHTMLFile(urlStr string) bool {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return false
	}

	path := parsedURL.Path
	return path == "" || strings.HasSuffix(path, "/") ||
		strings.HasSuffix(path, ".html") || strings.HasSuffix(path, ".htm") ||
		!strings.Contains(path, ".")
}

func (ctx *MirrorContext) shouldReject(urlStr string) bool {
	if len(ctx.RejectList) == 0 {
		return false
	}

	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return false
	}

	ext := strings.ToLower(filepath.Ext(parsedURL.Path))
	for _, rejectExt := range ctx.RejectList {
		if ext == rejectExt {
			return true
		}
	}

	return false
}

func (ctx *MirrorContext) isExcludedDirectory(urlStr string) bool {
	if len(ctx.ExcludeDirs) == 0 {
		return false
	}

	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return false
	}

	path := strings.Trim(parsedURL.Path, "/")
	for _, excludedDir := range ctx.ExcludeDirs {
		if strings.HasPrefix(path, excludedDir) {
			return true
		}
	}

	return false
}

func (ctx *MirrorContext) extractLinksFromNode(n *html.Node, baseURL string, depth int) {
	if n.Type == html.ElementNode {
		for _, attr := range n.Attr {
			switch attr.Key {
			case "href", "src", "action", "data-src", "data-href", "poster", "background":
				ctx.processFoundLink(attr.Val, baseURL, depth)
			case "srcset":
				ctx.processSrcSet(attr.Val, baseURL, depth)
			case "style":
				ctx.extractLinksFromCSS(attr.Val, baseURL, depth)
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		ctx.extractLinksFromNode(c, baseURL, depth)
	}
}

func (ctx *MirrorContext) parseHTMLForLinks(filePath, baseURL string, currentDepth int) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("Failed to read HTML file: %v", err)
		return
	}

	htmlContent := string(content)

	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		log.Printf("Failed to parse HTML: %v", err)
		return
	}

	ctx.extractBaseURL(doc, baseURL)
	ctx.extractLinksFromNode(doc, baseURL, currentDepth+1)
	ctx.extractLinksFromCSS(htmlContent, baseURL, currentDepth+1)
	ctx.extractLinksFromJavaScript(htmlContent, baseURL, currentDepth+1)
}

func (ctx *MirrorContext) processFoundLink(link, baseURL string, depth int) {
	if link == "" || strings.HasPrefix(link, "#") || strings.HasPrefix(link, "javascript:") {
		return
	}

	absoluteURL, err := ctx.resolveURLWithBase(link, baseURL)
	if err != nil {
		return
	}

	if !ctx.isSameDomain(absoluteURL) {
		return
	}

	if ctx.isExcludedDirectory(absoluteURL) {
		ctx.stats.TotalSkipped++
		return
	}

	if ctx.shouldReject(absoluteURL) {
		ctx.stats.TotalSkipped++
		return
	}

	if _, visited := ctx.VisitedURLs.Load(absoluteURL); visited {
		ctx.stats.TotalSkipped++
		return
	}

	ctx.enqueueTask(MirrorDownloadTask{
		URL:   absoluteURL,
		Depth: depth,
	})
}

func (ctx *MirrorContext) isSameDomain(urlStr string) bool {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return false
	}

	baseHost := strings.TrimPrefix(ctx.BaseURL.Host, "www.")
	targetHost := strings.TrimPrefix(parsedURL.Host, "www.")

	return baseHost == targetHost
}

func (ctx *MirrorContext) convertLinksInHTML(filePath, originalURL string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	htmlContent := string(content)

	htmlDir := filepath.Dir(filePath)
	outputBase := ctx.OutputDir

	patterns := []struct {
		pattern *regexp.Regexp
		replace string
	}{
		{
			regexp.MustCompile(`(?i)(href|src|action)=["'](\s*https?://[^"']+)["']`),
			`$1="$2"`,
		},
		{
			regexp.MustCompile(`(?i)url\((\s*https?://[^)]+)\)`),
			`url($1)`,
		},
	}

	for _, p := range patterns {
		htmlContent = p.pattern.ReplaceAllStringFunc(htmlContent, func(match string) string {
			parts := p.pattern.FindStringSubmatch(match)
			if len(parts) < 3 {
				return match
			}

			absoluteURL := parts[2]
			relativePath, err := ctx.getRelativePath(absoluteURL, htmlDir, outputBase)
			if err != nil {
				return match
			}

			return strings.Replace(match, absoluteURL, relativePath, 1)
		})
	}

	return os.WriteFile(filePath, []byte(htmlContent), 0644)
}

func (ctx *MirrorContext) getRelativePath(absoluteURL, htmlDir, outputBase string) (string, error) {

	if !ctx.isSameDomain(absoluteURL) {
		return absoluteURL, nil
	}

	localPath, err := ctx.getLocalPath(absoluteURL, false)
	if err != nil {
		return "", err
	}

	relativePath, err := filepath.Rel(htmlDir, localPath)
	if err != nil {
		return "", err
	}

	return filepath.ToSlash(relativePath), nil
}

func (ctx *MirrorContext) extractLinksFromCSS(cssContent, baseURL string, depth int) {
	re := regexp.MustCompile(`url\(['"]?([^'")]+)['"]?\)`)
	matches := re.FindAllStringSubmatch(cssContent, -1)

	for _, match := range matches {
		if len(match) > 1 {
			ctx.processFoundLink(match[1], baseURL, depth)
		}
	}
}

func (ctx *MirrorContext) extractLinksFromJavaScript(jsContent, baseURL string, depth int) {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`['"](/[^'"]+)['"]`),
		regexp.MustCompile(`['"](https?://[^'"]+)['"]`),
		regexp.MustCompile(`\.(href|src)\s*=\s*['"]([^'"]+)['"]`),
	}

	for _, pattern := range patterns {
		matches := pattern.FindAllStringSubmatch(jsContent, -1)
		for _, match := range matches {
			if len(match) > 1 {
				ctx.processFoundLink(match[1], baseURL, depth)
			}
		}
	}
}

func (ctx *MirrorContext) processSrcSet(srcset, baseURL string, depth int) {
	entries := strings.SplitSeq(srcset, ",")
	for entry := range entries {
		parts := strings.Fields(strings.TrimSpace(entry))
		if len(parts) > 0 {
			ctx.processFoundLink(parts[0], baseURL, depth)
		}
	}
}

func (ctx *MirrorContext) extractBaseURL(doc *html.Node, currentURL string) {
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "base" {
			for _, attr := range n.Attr {
				if attr.Key == "href" {
					baseURL, err := url.Parse(attr.Val)
					if err == nil {
						current, err := url.Parse(currentURL)
						if err == nil {
							ctx.mu.Lock()
							ctx.baseURL = current.ResolveReference(baseURL)
							ctx.mu.Unlock()
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)
}

func (ctx *MirrorContext) resolveURLWithBase(link, baseURL string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}

	ctx.mu.Lock()
	if ctx.baseURL != nil {
		base = ctx.baseURL
	}
	ctx.mu.Unlock()

	relative, err := url.Parse(link)
	if err != nil {
		return "", err
	}

	return base.ResolveReference(relative).String(), nil
}
