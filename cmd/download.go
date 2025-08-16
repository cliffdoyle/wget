package cmd

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func Download_file(link string) {
	// Build fileName from fullPath
	if strings.Trim(link, " ") == "" {
		log.Fatal("no link provided")
	}

	currentTime := time.Now()
	// Define the desired format using the Go reference time
	// "2006-01-02 15:04:05" corresponds to "yyyy-m-d h-m-s"
	formattedTime := currentTime.Format("2006-01-02 15:04:05")

	// Print the formatted time
	fmt.Printf("start at %v\n", formattedTime)
	fileURL, err := url.Parse(link)
	if err != nil {
		log.Fatal(err)
	}
	path := fileURL.Path
	segments := strings.Split(path, "/")
	fileName := segments[len(segments)-1]

	// Create blank file
	file, err := os.Create(fileName)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print("sending request, awaiting response ... ")
	client := http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			r.URL.Opaque = r.URL.Path
			return nil
		},
	}
	// Put content on file
	resp, err := client.Get(link)
	if resp.StatusCode != 200 {
		fmt.Printf("status %v", resp.Status)
		os.Exit(1)
	}

	fmt.Printf("status %v\n", resp.Status)
	if resp.ContentLength != -1 { // -1 indicates Content-Length is unknown or not present
		fmt.Printf("Content size: %d [~%s]\n", resp.ContentLength, BytesToMB(resp.ContentLength))
	} else {
		fmt.Println("Content-Length header not available or unknown.")
	}
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("saving file to: ./%v\n", fileName)
	defer resp.Body.Close()

	_, err = io.Copy(file, resp.Body)
	defer file.Close()
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
