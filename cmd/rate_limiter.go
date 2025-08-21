package cmd

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// SimpleRateLimitedWriter provides basic rate limiting without complex token logic
type SimpleRateLimitedWriter struct {
	writer       io.Writer
	bytesPerSec  int64
	startTime    time.Time
	bytesWritten int64
}

func NewSimpleRateLimitedWriter(writer io.Writer, bytesPerSec int64) *SimpleRateLimitedWriter {
	return &SimpleRateLimitedWriter{
		writer:      writer,
		bytesPerSec: bytesPerSec,
		startTime:   time.Now(),
	}
}

func (w *SimpleRateLimitedWriter) Write(p []byte) (int, error) {
	if w.bytesPerSec <= 0 {
		return w.writer.Write(p)
	}

	expectedTime := time.Duration(float64(w.bytesWritten+int64(len(p))) / float64(w.bytesPerSec) * float64(time.Second))
	elapsedTime := time.Since(w.startTime)

	if expectedTime > elapsedTime {
		sleepTime := expectedTime - elapsedTime
		time.Sleep(sleepTime)
	}

	n, err := w.writer.Write(p)
	w.bytesWritten += int64(n)
	return n, err
}

// ParseRateLimit converts string like "200k" or "2M" to bytes per second
func ParseRateLimit(rateStr string) (int64, error) {
	if rateStr == "" {
		return 0, nil
	}

	rateStr = strings.ToLower(strings.TrimSpace(rateStr))

	var numericPart string
	var unit string

	for i, char := range rateStr {
		if char < '0' || char > '9' {
			numericPart = rateStr[:i]
			unit = rateStr[i:]
			break
		}
	}

	if numericPart == "" {
		numericPart = rateStr
	}

	value, err := strconv.ParseFloat(numericPart, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid rate limit format: %s", rateStr)
	}

	switch unit {
	case "", "b", "bps":
	case "k", "kb", "kbps":
		value *= 1024
	case "m", "mb", "mbps":
		value *= 1024 * 1024
	case "g", "gb", "gbps":
		value *= 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown rate limit unit: %s", unit)
	}

	if value <= 0 {
		return 0, fmt.Errorf("rate limit must be positive")
	}

	return int64(value), nil
}

// GetRateLimitDisplay returns human-readable rate limit string
func GetRateLimitDisplay(rateLimit int64) string {
	if rateLimit == 0 {
		return "unlimited"
	}

	switch {
	case rateLimit >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB/s", float64(rateLimit)/(1024*1024*1024))
	case rateLimit >= 1024*1024:
		return fmt.Sprintf("%.1f MB/s", float64(rateLimit)/(1024*1024))
	case rateLimit >= 1024:
		return fmt.Sprintf("%.1f KB/s", float64(rateLimit)/1024)
	default:
		return fmt.Sprintf("%d B/s", rateLimit)
	}
}
