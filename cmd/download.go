package cmd

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func Download_file(link string) {
	// Build fileName from fullPath
	if strings.Trim(link, " ") == "" {
		log.Fatal("no link provided")
	}

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
	client := http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			r.URL.Opaque = r.URL.Path
			return nil
		},
	}
	// Put content on file
	resp, err := client.Get(link)
	if resp.ContentLength != -1 { // -1 indicates Content-Length is unknown or not present
		fmt.Printf("Content-Length: %d bytes\n", resp.ContentLength)
	} else {
		fmt.Println("Content-Length header not available or unknown.")
	}
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	_, err = io.Copy(file, resp.Body)

	defer file.Close()
}

func HumanSize(size int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)

	switch {
	case size < KB:
		return fmt.Sprintf("%d B", size)
	case size < MB:
		return fmt.Sprintf("%.1f KiB", float64(size)/KB)
	case size < GB:
		return fmt.Sprintf("%.1f MiB", float64(size)/MB)
	case size < TB:
		return fmt.Sprintf("%.1f GiB", float64(size)/GB)
	default:
		return fmt.Sprintf("%.1f TiB", float64(size)/TB)
	}
}
