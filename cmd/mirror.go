package cmd

import (
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

// MirrorContext holds the state for website mirroring
type MirrorContext struct {
	BaseURL       *url.URL
	OutputDir     string
	VisitedURLs   sync.Map
	RejectList    []string
	ExcludeDirs   []string
	ConvertLinks  bool
	MaxDepth      int
	DownloadQueue chan MirrorDownloadTask
	WaitGroup     sync.WaitGroup
	Client        *http.Client
}

// DownloadTask represents a mirroring download task
type MirrorDownloadTask struct {
	URL    string
	Depth  int
	IsPage bool
}

// InitMirroring initializes the mirroring process
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
			rejectList[i] = strings.TrimSpace(ext)
			if !strings.HasPrefix(ext, ".") {
				rejectList[i] = "." + ext
			}
			rejectList[i] = strings.ToLower(rejectList[i])
		}
	}

	var excludeDirs []string
	if MirrorFlagsConfig.Exclude != "" {
		excludeDirs = strings.Split(MirrorFlagsConfig.Exclude, ",")
		for i, dir := range excludeDirs {
			excludeDirs[i] = strings.TrimSpace(dir)
		}
	}

	ctx := &MirrorContext{
		BaseURL:       parsedURL,
		OutputDir:     outputDir,
		RejectList:    rejectList,
		ExcludeDirs:   excludeDirs,
		ConvertLinks:  MirrorFlagsConfig.ConvertLinks,
		MaxDepth:      MirrorFlagsConfig.Depth,
		DownloadQueue: make(chan MirrorDownloadTask, 1000),
		Client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
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

	numWorkers := 5
	for i := range numWorkers {
		ctx.WaitGroup.Add(1)
		go ctx.mirrorWorker(i + 1)
	}

	go func() {
		ctx.DownloadQueue <- MirrorDownloadTask{
			URL:    startURL,
			Depth:  0,
			IsPage: true,
		}
	}()

	close(ctx.DownloadQueue)
	ctx.WaitGroup.Wait()

	fmt.Printf("\nMirroring completed! Saved to %s/\n", outputDir)
	return nil
}

// mirrorWorker processes mirroring download tasks
func (ctx *MirrorContext) mirrorWorker(workerID int) {
	defer ctx.WaitGroup.Done()

	for task := range ctx.DownloadQueue {
		if task.Depth > ctx.MaxDepth {
			continue
		}
		if _, visited := ctx.VisitedURLs.Load(task.URL); visited {
			continue
		}
		ctx.VisitedURLs.Store(task.URL, true)

		fmt.Printf("[Worker %d] Depth %d: %s\n", workerID, task.Depth, task.URL)

		filePath, err := ctx.downloadResource(task.URL, task.IsPage)
		if err != nil {
			log.Printf("Failed to download %s: %v", task.URL, err)
			continue
		}

		if task.IsPage && strings.HasSuffix(filePath, ".html") {
			ctx.parseHTMLForLinks(filePath, task.URL, task.Depth+1)
		}
	}
}

// downloadResource downloads a single resource for mirroring
func (ctx *MirrorContext) downloadResource(urlStr string, isPage bool) (string, error) {
	if ctx.shouldReject(urlStr) {
		return "", fmt.Errorf("rejected by extension filter")
	}
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Wget/1.0")

	resp, err := ctx.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	filePath, err := ctx.getLocalPath(urlStr, isPage)
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

	return filePath, nil
}

// shouldReject checks if a URL should be rejected based on extension
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
		if ext == strings.ToLower(rejectExt) {
			return true
		}
	}

	return false
}

// getLocalPath converts a URL to a local file path
func (ctx *MirrorContext) getLocalPath(urlStr string, isPage bool) (string, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}

	localPath := filepath.Join(ctx.OutputDir, parsedURL.Host, parsedURL.Path)

	if isPage && !strings.Contains(localPath, ".") {
		localPath = filepath.Join(localPath, "index.html")
	} else if isPage && !strings.HasSuffix(localPath, ".html") {
		localPath += ".html"
	}

	return localPath, nil
}

// parseHTMLForLinks extracts links from HTML content
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
			case "img", "script":
				linkAttr = "src"
			}

			if linkAttr != "" {
				for _, attr := range n.Attr {
					if attr.Key == linkAttr {
						ctx.processFoundLink(attr.Val, baseURL, depth)
					}
				}
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extractLinks(c)
		}
	}

	extractLinks(doc)

	// TODO: Implement link conversion if ctx.ConvertLinks is true
}

// processFoundLink processes a discovered link
func (ctx *MirrorContext) processFoundLink(link, baseURL string, depth int) {
	if link == "" || strings.HasPrefix(link, "#") {
		return
	}

	absoluteURL, err := ctx.resolveURL(link, baseURL)
	if err != nil {
		return
	}

	if !ctx.isSameDomain(absoluteURL) {
		return
	}

	if ctx.isExcludedDirectory(absoluteURL) {
		return
	}

	isPage := strings.Contains(absoluteURL, ".html") || !strings.Contains(absoluteURL, ".")
	ctx.DownloadQueue <- MirrorDownloadTask{
		URL:    absoluteURL,
		Depth:  depth,
		IsPage: isPage,
	}
}

// resolveURL converts relative URL to absolute
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

// isSameDomain checks if URL belongs to the same domain
func (ctx *MirrorContext) isSameDomain(urlStr string) bool {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return false
	}

	// Remove www. prefix for comparison
	baseHost := strings.TrimPrefix(ctx.BaseURL.Host, "www.")
	targetHost := strings.TrimPrefix(parsedURL.Host, "www.")

	return baseHost == targetHost
}

// isExcludedDirectory checks if URL path matches excluded directories
func (ctx *MirrorContext) isExcludedDirectory(urlStr string) bool {
	if len(ctx.ExcludeDirs) == 0 {
		return false
	}

	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return false
	}

	for _, excludedDir := range ctx.ExcludeDirs {
		excludedDir = strings.Trim(excludedDir, "/")
		if excludedDir != "" && strings.HasPrefix(strings.Trim(parsedURL.Path, "/"), excludedDir) {
			return true
		}
	}

	return false
}
