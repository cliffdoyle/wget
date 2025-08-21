package cmd

import (
	"context"
	"errors"
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

	"github.com/schollz/progressbar/v3"
)

type ThreadSafeProgressWriter struct {
	bar    *progressbar.ProgressBar
	mu     sync.Mutex
	writer io.Writer
}

func NewThreadSafeProgressWriter(bar *progressbar.ProgressBar) *ThreadSafeProgressWriter {
	return &ThreadSafeProgressWriter{
		bar:    bar,
		writer: io.MultiWriter(bar),
	}
}

func (tpw *ThreadSafeProgressWriter) Write(p []byte) (int, error) {
	tpw.mu.Lock()
	defer tpw.mu.Unlock()

	n, err := tpw.writer.Write(p)
	if err != nil {
		return n, err
	}

	tpw.bar.Add(len(p))
	return n, nil
}

func Download_file(link string) {
	if strings.Trim(link, " ") == "" {
		log.Fatal("no link provided")
	}

	var message string
	if Down.Bflag {
		fmt.Println("Output will be written to \"wget-log\".")
	}

	startTime := GetTime()
	LogMessage(fmt.Sprintf("start at %v\n", startTime))

	var rateLimit int64
	var err error
	if Down.RateLimit != "" {
		rateLimit, err = ParseRateLimit(Down.RateLimit)
		if err != nil {
			log.Fatal("Invalid rate limit: ", err)
		}
		LogMessage(fmt.Sprintf("Rate limit set to: %s\n", GetRateLimitDisplay(rateLimit)))
	}

	fileURL, err := url.Parse(link)
	if err != nil {
		log.Fatal("Invalid URL: ", err)
	}

	path := fileURL.Path
	segments := strings.Split(path, "/")
	fileName := segments[len(segments)-1]

	if strings.TrimSpace(Down.Oflag) != "" {
		fileName = Down.Oflag
	}

	if strings.TrimSpace(Down.Pflag) != "" {
		fileName = filepath.Join(Down.Pflag, fileName)
	}

	file, err := os.Create(fileName)
	if err != nil {
		log.Fatal("Cannot create file: ", err)
	}
	defer file.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", link, nil)
	if err != nil {
		log.Fatal("Failed to create request: ", err)
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
		if errors.Is(err, context.DeadlineExceeded) {
			log.Fatal("Request timed out")
		}
		log.Fatal("Download failed: ", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		LogMessage(fmt.Sprintf("status %v\n", resp.Status))
		os.Exit(1)
	}

	LogMessage(fmt.Sprintf("sending request, awaiting response... status %v\n", resp.Status))

	if resp.ContentLength != -1 {
		message = fmt.Sprintf("content size: %d [~%s]\n", resp.ContentLength, BytesToMB(resp.ContentLength))
	} else {
		message = "Content-Length header not available or unknown.\n"
	}
	LogMessage(message)
	LogMessage(fmt.Sprintf("saving file to: ./%s\n", fileName))

	var writer io.Writer = file

	if rateLimit > 0 {
		writer = NewSimpleRateLimitedWriter(file, rateLimit)
	}

	if !Down.Bflag {
		bar := progressbar.NewOptions64(
			resp.ContentLength,
			progressbar.OptionSetDescription("downloading"),
			progressbar.OptionShowBytes(true),
			progressbar.OptionSetWidth(35),
			progressbar.OptionShowCount(),
			progressbar.OptionSetElapsedTime(false),
			progressbar.OptionSetPredictTime(false),
			progressbar.OptionOnCompletion(func() {
				fmt.Print("\n")
			}),
			progressbar.OptionFullWidth(),
			progressbar.OptionSetRenderBlankState(true),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "=",
				SaucerHead:    ">",
				SaucerPadding: " ",
				BarStart:      "[",
				BarEnd:        "]",
			}),
		)

		multiWriter := io.MultiWriter(writer, bar)

		_, err = io.Copy(multiWriter, resp.Body)
		if err != nil {
			log.Fatal("Download failed: ", err)
		}

		bar.Finish()
		fmt.Println()

	} else {
		_, err = io.Copy(writer, resp.Body)
		if err != nil {
			log.Fatal("Download failed: ", err)
		}
	}

	LogMessage(fmt.Sprintf("Downloaded [%v]\n", link))
	LogMessage(fmt.Sprintf("finished at %v\n", GetTime()))
}

// BytesToKB converts bytes to kilobytes (KiB = 1024 bytes) and rounds to 2 decimals
func BytesToKB(size int64) string {
	kb := float64(size) / 1024
	return fmt.Sprintf("%.2fKB", kb)
}

// BytesToMB converts bytes to megabytes (MiB = 1024 * 1024 bytes) and rounds to 2 decimals
func BytesToMB(size int64) string {
	mb := float64(size) / (1024 * 1024)
	return fmt.Sprintf("%.2fMB", mb)
}

func GetTime() string {
	currentTime := time.Now()
	// "2006-01-02 15:04:05" corresponds to "yyyy-m-d h-m-s"
	formattedTime := currentTime.Format("2006-01-02 15:04:05")

	// return the formatted time
	return formattedTime
}
func LogMessage(message string) {
	if Down.Bflag {
		logToFile(message)
	} else {
		fmt.Print(message)
	}
}

func logToFileConcurrent(message string) {
	file, err := os.OpenFile("wget-log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)
	if err != nil {
		return
	}
	defer file.Close()

	logger := log.New(file, "", 0)
	logger.Print(message)
}
