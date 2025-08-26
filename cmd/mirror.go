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
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

type MirrorContext struct {
	BaseURL      *url.URL
	OutputDir    string
	VisitedURLs  sync.Map
	RejectList   []string
	ExcludeDirs  []string
	ConvertLinks bool
	MaxDepth     int
	Client       *http.Client

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

	// Build reject list
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
			dir = strings.Trim(strings.TrimSpace(dir), "/")
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
		Client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return http.ErrUseLastResponse
				}
				return nil
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

	ctx.enqueueTask(MirrorDownloadTask{URL: startURL, Depth: 0})
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

	ctx.VisitedURLs.Store(task.URL, true)
	ctx.workQueue.PushBack(task)
	ctx.stats.TotalDiscovered++
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
	for i := 0; i < ctx.maxWorkers; i++ {
		ctx.waitGroup.Add(1)
		ctx.activeWorkers++
		go ctx.mirrorWorker(i + 1)
	}
}

func (ctx *MirrorContext) isWorkComplete() bool {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	return ctx.workQueue.Len() == 0 && ctx.activeWorkers == 0
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

func (ctx *MirrorContext) processDownloadTask(task MirrorDownloadTask, workerID int) {
	filePath, err := ctx.downloadResource(task.URL)
	if err != nil {
		ctx.stats.TotalFailed++
		log.Printf("Failed to download %s: %v", task.URL, err)
		return
	}
	ctx.stats.TotalDownloaded++

	if ctx.isHTMLFile(task.URL) || strings.HasSuffix(filePath, ".html") {
		fmt.Printf("[Worker %d] Depth %d: %s (HTML)\n", workerID, task.Depth, task.URL)
		ctx.parseHTMLForLinks(filePath, task.URL, task.Depth+1)
	} else {
		fmt.Printf("[Worker %d] Depth %d: %s (Resource)\n", workerID, task.Depth, task.URL)
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

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
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
		if err := ctx.rewriteLinksInHTML(filePath); err != nil {
			log.Printf("Link rewriting failed for %s: %v", filePath, err)
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
	return filepath.Clean(filepath.Join(ctx.OutputDir, path)), nil
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
	ext := strings.ToLower(filepath.Ext(strings.Split(parsedURL.Path, "?")[0]))
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
	parts := strings.Split(strings.Trim(parsedURL.Path, "/"), "/")
	for _, excluded := range ctx.ExcludeDirs {
		for _, part := range parts {
			if part == excluded {
				return true
			}
		}
	}
	return false
}

func (ctx *MirrorContext) parseHTMLForLinks(filePath, baseURL string, currentDepth int) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	doc, err := html.Parse(strings.NewReader(string(content)))
	if err != nil {
		return
	}
	ctx.extractBaseURL(doc, baseURL)
	ctx.extractLinksFromNode(doc, baseURL, currentDepth)
}

func (ctx *MirrorContext) extractLinksFromNode(n *html.Node, baseURL string, depth int) {
	if n.Type == html.ElementNode {
		for _, attr := range n.Attr {
			switch attr.Key {
			case "href", "src", "action", "data-src", "data-href", "poster", "background":
				ctx.processFoundLink(attr.Val, baseURL, depth)
			case "srcset":
				for _, entry := range strings.Split(attr.Val, ",") {
					parts := strings.Fields(strings.TrimSpace(entry))
					if len(parts) > 0 {
						ctx.processFoundLink(parts[0], baseURL, depth)
					}
				}
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		ctx.extractLinksFromNode(c, baseURL, depth)
	}
}

func (ctx *MirrorContext) processFoundLink(link, baseURL string, depth int) {
	if link == "" || strings.HasPrefix(link, "#") || strings.HasPrefix(link, "javascript:") {
		return
	}
	absoluteURL, err := ctx.resolveURLWithBase(link, baseURL)
	if err != nil || !ctx.isSameDomain(absoluteURL) {
		return
	}
	if ctx.isExcludedDirectory(absoluteURL) || ctx.shouldReject(absoluteURL) {
		ctx.stats.TotalSkipped++
		return
	}
	if _, visited := ctx.VisitedURLs.Load(absoluteURL); visited {
		ctx.stats.TotalSkipped++
		return
	}
	ctx.enqueueTask(MirrorDownloadTask{URL: absoluteURL, Depth: depth})
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

func (ctx *MirrorContext) extractBaseURL(doc *html.Node, currentURL string) {
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "base" {
			for _, attr := range n.Attr {
				if attr.Key == "href" {
					if baseURL, err := url.Parse(attr.Val); err == nil {
						if current, err := url.Parse(currentURL); err == nil {
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

func (ctx *MirrorContext) rewriteLinksInHTML(filePath string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	doc, err := html.Parse(strings.NewReader(string(content)))
	if err != nil {
		return err
	}
	var rewrite func(*html.Node)
	htmlDir := filepath.Dir(filePath)
	rewrite = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for i, attr := range n.Attr {
				if attr.Key == "href" || attr.Key == "src" {
					if rel, err := ctx.getRelativePath(attr.Val, htmlDir); err == nil {
						n.Attr[i].Val = rel
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			rewrite(c)
		}
	}
	rewrite(doc)

	outFile, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer outFile.Close()
	return html.Render(outFile, doc)
}

func (ctx *MirrorContext) getRelativePath(absoluteURL, htmlDir string) (string, error) {
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

func (ctx *MirrorContext) printStatistics() {
	elapsed := time.Since(ctx.stats.StartTime)
	fmt.Printf("\n=== Mirroring Statistics ===\n")
	fmt.Printf("Total URLs discovered: %d\n", ctx.stats.TotalDiscovered)
	fmt.Printf("Successfully downloaded: %d\n", ctx.stats.TotalDownloaded)
	fmt.Printf("Failed downloads: %d\n", ctx.stats.TotalFailed)
	fmt.Printf("Skipped URLs: %d\n", ctx.stats.TotalSkipped)
	fmt.Printf("Time elapsed: %v\n", elapsed.Round(time.Second))
}
