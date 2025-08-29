# wget (Go)

A simple wget-like command-line tool written in Go. This project provides basic functionality to download files from the internet, with support for concurrent downloads, rate limiting, and mirroring.

## Features
- Download files from HTTP/HTTPS URLs
- Concurrent downloads
- Rate limiting
- Mirroring directories
- Logging

## Usage

Build the project:

```bash
go build -o wget
```

Run the tool:

```bash
./wget [flags] <url>
```

### Example

Download a file:

```bash
./wget https://example.com/file.zip
```

Download with concurrency:

```bash
./wget --concurrent 4 https://example.com/file.zip
```

Mirror a website:

```bash
./wget --mirror https://example.com/
```

## Flags
- `--concurrent` : Number of concurrent downloads
- `--rate` : Download rate limit (e.g., 500k, 2M)
- `--mirror` : Enable mirroring mode
- `--output` : Output file or directory
- `--log` : Enable logging

## Project Structure
- `main.go` : Entry point
- `cmd/` : Command implementations
    - `concurrent.go` : Concurrency logic
    - `download.go` : Download logic
    - `logging.go` : Logging utilities
    - `mirror.go` : Mirroring logic
    - `rate_limiter.go` : Rate limiting logic
    - `root.go` : Root command and flag parsing

## Requirements
- Go 1.18+

## License
MIT
