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
		BaseURL:       parsedURL,
		OutputDir:     outputDir,
		RejectList:    rejectList,
		ExcludeDirs:   excludeDirs,
		ConvertLinks:  MirrorFlagsConfig.ConvertLinks,
		MaxDepth:      MirrorFlagsConfig.Depth,
		DownloadQueue: make(chan MirrorDownloadTask, 100),
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

	numWorkers := 5
	for i := range numWorkers {
		go ctx.mirrorWorker(i + 1)
	}

	// add waitgroup to track when a task is assigned
	ctx.WaitGroup.Add(1)
	ctx.DownloadQueue <- MirrorDownloadTask{
		URL:   startURL,
		Depth: 0,
	}

	// the main loop should wait and close the channel once done*
	ctx.WaitGroup.Wait()
	close(ctx.DownloadQueue)

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

	if ctx.activeWorkers < ctx.maxWorkers {
		ctx.waitGroup.Add(1)
		ctx.activeWorkers++
		go ctx.mirrorWorker(ctx.activeWorkers)
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

func (ctx *MirrorContext) mirrorWorker(workerID int) {
	//the worker should report done when finished with a task, we track when work is done
	for task := range ctx.DownloadQueue {
		ctx.processDownloadTask(task, workerID)
		ctx.WaitGroup.Done()
	}
}

func (ctx *MirrorContext) processDownloadTask(task MirrorDownloadTask, workerID int) {
	if task.Depth > ctx.MaxDepth {
		return
	}

	if _, visited := ctx.VisitedURLs.Load(task.URL); visited {
		return
	}
	ctx.VisitedURLs.Store(task.URL, true)

	fmt.Printf("[Worker %d] Depth %d: %s\n", workerID, task.Depth, task.URL)

	filePath, err := ctx.downloadResource(task.URL)
	if err != nil {
		log.Printf("Failed to download %s: %v", task.URL, err)
		return
	}

	if ctx.isHTMLFile(task.URL) || strings.HasSuffix(filePath, ".html") {
		ctx.parseHTMLForLinks(filePath, task.URL, task.Depth+1)
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
		if err := ctx.convertLinksInHTML(filePath); err != nil {
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

func (ctx *MirrorContext) parseHTMLForLinks(filePath, baseURL string, depth int) {
	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("Failed to open HTML file: %v", err)
		return
	}
	defer file.Close()

	doc, err := html.Parse(file)
	if err != nil {
		log.Printf("Failed to parse HTML: %v", err)
		return
	}

	var extractLinks func(*html.Node)
	extractLinks = func(n *html.Node) {
		if n.Type == html.ElementNode {
			var linkAttr string
			switch n.Data {
			case "a", "link":
				linkAttr = "href"
			case "img", "script", "iframe":
				linkAttr = "src"
			case "form":
				linkAttr = "action"
			}

			for _, attr := range n.Attr {
				// main link attribute
				if linkAttr != "" && attr.Key == linkAttr {
					ctx.processFoundLink(attr.Val, baseURL, depth)
				}

				//this part handles tags with the style attribute
				if attr.Key == "style" {
					re := regexp.MustCompile(`url\(([^)]+)\)`)
					matches := re.FindAllStringSubmatch(attr.Val, -1)
					for _, m := range matches {
						rawUrl := strings.Trim(m[1], `"'`)
						ctx.processFoundLink(rawUrl, baseURL, depth)
					}
				}
			}

			//this part handles links inside the textcontent of the style div
			if n.Data == "style" {
				var cssContent string
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						cssContent += c.Data
					}
				}
				re := regexp.MustCompile(`url\(([^)]+)\)`)
				matches := re.FindAllStringSubmatch(cssContent, -1)
				for _, m := range matches {
					rawUrl := strings.Trim(m[1], `"'`)
					ctx.processFoundLink(rawUrl, baseURL, depth)
				}
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extractLinks(c)
		}
	}

	extractLinks(doc)
}

func (ctx *MirrorContext) processFoundLink(link, baseURL string, depth int) {
	if link == "" || strings.HasPrefix(link, "#") || strings.HasPrefix(link, "javascript:") {
		return
	}

	absoluteURL, err := ctx.resolveURL(link, baseURL)
	if err != nil {
		return
	}

	if !ctx.isSameDomain(absoluteURL) {
		return
	}

	if _, visited := ctx.VisitedURLs.Load(absoluteURL); visited {
		return
	}

	ctx.mu.Lock()
	// add wait when a task is added, the worker should remove when done
	ctx.WaitGroup.Add(1)
	ctx.DownloadQueue <- MirrorDownloadTask{
		URL:   absoluteURL,
		Depth: depth,
	}
	ctx.mu.Unlock()
}

func (ctx *MirrorContext) resolveURL(link, baseURL string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}

	relative, err := url.Parse(link)
	if err != nil {
		return "", err
	}

	return base.ResolveReference(relative).String(), nil
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

func (ctx *MirrorContext) convertLinksInHTML(filePath string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	htmlContent := string(content)
	baseURL := ctx.BaseURL.String()
	baseHost := ctx.BaseURL.Host

	replacements := []struct {
		old string
		new string
	}{
		{baseURL, ""},
		{"https://" + baseHost, ""},
		{"http://" + baseHost, ""},
		{"//" + baseHost, ""},
	}

	for _, r := range replacements {
		htmlContent = strings.ReplaceAll(htmlContent, r.old, r.new)
	}

	return os.WriteFile(filePath, []byte(htmlContent), 0644)
}
