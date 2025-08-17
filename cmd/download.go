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
	"time"

	"github.com/schollz/progressbar/v3"
)

func Download_file(link string) {
	if strings.Trim(link, " ") == "" {
		log.Fatal("no link provided")
	}
	var message string

	//check for b flag to decide where the output goes
	if Down.Bflag {
		fmt.Println("Output will be written to \"wget-log\".")
	}

	LogMessage(fmt.Sprintf("start at %v\n", GetTime()))
	fileURL, err := url.Parse(link)
	if err != nil {
		log.Fatal(err)
	}
	path := fileURL.Path
	segments := strings.Split(path, "/")
	fileName := segments[len(segments)-1]
	// if the flag flag is provided we change it to the provided flag
	if strings.TrimSpace(Down.Oflag) != "" {
		fileName = Down.Oflag
	}

	//check if a filepath has been passed with the p flag
	if strings.TrimSpace(Down.Pflag) != "" {
		fileName = filepath.Join(Down.Pflag, fileName)
	}
	// Create blank file
	file, err := os.Create(fileName)
	if err != nil {
		log.Fatal(err)
	}
	client := http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			r.URL.Opaque = r.URL.Path
			return nil
		},
	}
	// Put content on file
	resp, err := client.Get(link)
	if resp.StatusCode != 200 {
		LogMessage(fmt.Sprintf("status %v", resp.Status))
		os.Exit(1)
	}

	LogMessage(fmt.Sprintf("sending request, awaiting response ... status %v\n", resp.Status))
	if resp.ContentLength != -1 { // -1 indicates Content-Length is unknown or not present
		message = fmt.Sprintf("Content size: %d [~%s]\n", resp.ContentLength, BytesToMB(resp.ContentLength))
	} else {
		message = "Content-Length header not available or unknown.\n"
	}

	//err check for resp
	if err != nil {
		log.Fatal(err)
	}
	LogMessage(message)

	LogMessage(fmt.Sprintf("saving file to: ./%v\n", fileName))
	defer resp.Body.Close()
	// if background is not passed
	if !Down.Bflag {
		bar := progressbar.NewOptions64(
			resp.ContentLength,
			progressbar.OptionSetDescription("downloading"),
			progressbar.OptionShowBytes(true),
			progressbar.OptionSetWidth(35),
			progressbar.OptionShowCount(),
			progressbar.OptionSetElapsedTime(true), // time
			progressbar.OptionSetPredictTime(true), // ETA
			progressbar.OptionOnCompletion(func() {
				fmt.Println()
			}),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "=",
				SaucerHead:    ">",
				SaucerPadding: " ",
				BarStart:      "[",
				BarEnd:        "]",
			}),
		)
		io.Copy(io.MultiWriter(file, bar), resp.Body)

		defer bar.Clear()
		defer file.Close()
	} else {
		io.Copy(file, resp.Body)
		defer file.Close()
	}
	LogMessage(fmt.Sprintf("Downloaded [%v]\nfinished at %v\n", link, GetTime()))
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
