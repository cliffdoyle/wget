package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	uri "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// readURLsFromFile reads URLs from a text file (one per line)
func readURLsFromFile(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open input file: %w", err)
	}
	defer file.Close()

	var urls []string
	scanner := bufio.NewScanner(file)
	lineNumber := 0

	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if !strings.HasPrefix(line, "http://") && !strings.HasPrefix(line, "https://") {
			return nil, fmt.Errorf("invalid URL on line %d: %s (must start with http:// or https://)", lineNumber, line)
		}

		urls = append(urls, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading input file: %w", err)
	}

	if len(urls) == 0 {
		return nil, fmt.Errorf("no valid URLs found in input file")
	}

	return urls, nil
}

// DownloadResult represents the result of a download attempt
type DownloadResult struct {
	URL     string
	Success bool
	Error   error
	Size    int64
	Elapsed time.Duration
}

// downloadSingle is a modified version of Download_file that returns results instead of fatal errors
func downloadSingle(url string) DownloadResult {
	startTime := time.Now()

	var output strings.Builder
	logCapture := func(message string) {
		if Down.Bflag {
			logToFile(message)
		} else {
			output.WriteString(message)
		}
	}

	if strings.Trim(url, " ") == "" {
		return DownloadResult{
			URL:     url,
			Success: false,
			Error:   fmt.Errorf("empty URL"),
		}
	}

	fileURL, err := uri.Parse(url)
	if err != nil {
		return DownloadResult{
			URL:     url,
			Success: false,
			Error:   fmt.Errorf("invalid URL: %w", err),
		}
	}

	path := fileURL.Path
	segments := strings.Split(path, "/")
	fileName := segments[len(segments)-1]
	if fileName == "" {
		fileName = "index.html"
	}

	if strings.TrimSpace(Down.Oflag) != "" {
		fileName = Down.Oflag
	}
	if strings.TrimSpace(Down.Pflag) != "" {
		fileName = filepath.Join(Down.Pflag, fileName)
	}

	// Create file
	file, err := os.Create(fileName)
	if err != nil {
		return DownloadResult{
			URL:     url,
			Success: false,
			Error:   fmt.Errorf("cannot create file: %w", err),
		}
	}
	defer file.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return DownloadResult{
			URL:     url,
			Success: false,
			Error:   fmt.Errorf("failed to create request: %w", err),
		}
	}

	client := http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			r.URL.Opaque = r.URL.Path
			return nil
		},
		Transport: &http.Transport{
			ResponseHeaderTimeout: 10 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return DownloadResult{
			URL:     url,
			Success: false,
			Error:   fmt.Errorf("download failed: %w", err),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return DownloadResult{
			URL:     url,
			Success: false,
			Error:   fmt.Errorf("HTTP status %s", resp.Status),
		}
	}

	var writer io.Writer = file
	var rateLimit int64

	if Down.RateLimit != "" {
		rateLimit, err = ParseRateLimit(Down.RateLimit)
		if err == nil && rateLimit > 0 {
			writer = NewSimpleRateLimitedWriter(file, rateLimit)
		}
	}

	_, err = io.Copy(writer, resp.Body)
	elapsed := time.Since(startTime)

	if err != nil {
		return DownloadResult{
			URL:     url,
			Success: false,
			Error:   fmt.Errorf("copy failed: %w", err),
			Elapsed: elapsed,
		}
	}

	fileInfo, err := file.Stat()
	size := int64(0)
	if err == nil {
		size = fileInfo.Size()
	}

	logCapture(fmt.Sprintf("Downloaded [%v] in %v (%s)\n", url, elapsed, BytesToMB(size)))

	return DownloadResult{
		URL:     url,
		Success: true,
		Size:    size,
		Elapsed: elapsed,
	}
}

// DownloadConcurrently handles the -i flag for multiple concurrent downloads
func DownloadConcurrently(inputFile string) {
	fmt.Printf("Reading URLs from: %s\n", inputFile)

	urls, err := readURLsFromFile(inputFile)
	if err != nil {
		log.Fatal("Error reading input file: ", err)
	}

	fmt.Printf("Found %d URLs to download concurrently\n\n", len(urls))

	maxConcurrent := min(len(urls), 5)

	urlChan := make(chan string, len(urls))
	resultsChan := make(chan DownloadResult, len(urls))

	var wg sync.WaitGroup

	for i := 0; i < maxConcurrent; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for url := range urlChan {
				fmt.Printf("[Worker %d] Starting: %s\n", workerID, url)
				result := downloadSingle(url)
				resultsChan <- result
			}
		}(i + 1)
	}

	for _, url := range urls {
		urlChan <- url
	}
	close(urlChan)

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	var results []DownloadResult
	successCount := 0
	failCount := 0

	for result := range resultsChan {
		results = append(results, result)
		if result.Success {
			successCount++
			fmt.Printf("Success: %s (%s in %v)\n",
				result.URL, BytesToMB(result.Size), result.Elapsed)
		} else {
			failCount++
			fmt.Printf("Failed: %s - %v\n", result.URL, result.Error)
		}
	}

	fmt.Printf("\n=== Download Summary ===\n")
	fmt.Printf("Total URLs: %d\n", len(urls))
	fmt.Printf("Successful: %d\n", successCount)
	fmt.Printf("Failed: %d\n", failCount)
	fmt.Printf("Success rate: %.1f%%\n", float64(successCount)/float64(len(urls))*100)
}
