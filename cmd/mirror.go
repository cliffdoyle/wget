package cmd

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/html"
)

type mirror struct {
	baseURL     *url.URL
	outputDir   string
	client      *http.Client
	ctx         context.Context
	cancel      context.CancelFunc
	workers     int
	tasks       chan task
	visited     sync.Map
	rejectExts  []string
	excludeDirs []string
	convert     bool
	maxDepth    int
	maxPages    int
	tasksClosed uint32

	stats   mirrorStats
	statsMu sync.Mutex
}

type task struct {
	url   string
	depth int
}

type mirrorStats struct {
	discovered int
	downloaded int
	failed     int
	skipped    int
	start      time.Time
}

// InitMirroring initializes the mirroring process for the given startURL.
// It parses the URL, creates the output directory, and sets up rejection rules.
func InitMirroring(startURL string) error {
	parsed, err := url.Parse(startURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	out := parsed.Host
	if err := os.MkdirAll(out, 0755); err != nil {
		return fmt.Errorf("failed to create output dir: %w", err)
	}

	var rej []string
	if MirrorFlagsConfig.Reject != "" {
		for e := range strings.SplitSeq(MirrorFlagsConfig.Reject, ",") {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			if !strings.HasPrefix(e, ".") {
				e = "." + e
			}
			rej = append(rej, strings.ToLower(e))
		}
	}
	var excl []string
	if MirrorFlagsConfig.Exclude != "" {
		for d := range strings.SplitSeq(MirrorFlagsConfig.Exclude, ",") {
			d = strings.Trim(strings.TrimSpace(d), "/")
			if d != "" {
				excl = append(excl, d)
			}
		}
	}

	var rootCtx context.Context
	var cancel context.CancelFunc
	if MirrorFlagsConfig.Timeout > 0 {
		rootCtx, cancel = context.WithTimeout(context.Background(), time.Duration(MirrorFlagsConfig.Timeout)*time.Second)
	} else {
		rootCtx, cancel = context.WithCancel(context.Background())
	}

	client := &http.Client{Timeout: 0}

	m := &mirror{
		baseURL:     parsed,
		outputDir:   out,
		client:      client,
		ctx:         rootCtx,
		cancel:      cancel,
		workers:     runtime.GOMAXPROCS(0) * 2,
		tasks:       make(chan task, 1024),
		rejectExts:  rej,
		excludeDirs: excl,
		convert:     MirrorFlagsConfig.ConvertLinks,
		maxDepth:    MirrorFlagsConfig.Depth,
		maxPages:    MirrorFlagsConfig.MaxPages,
	}
	m.stats.start = time.Now()

	fmt.Printf("Mirroring %s -> %s/ (depth=%d, convert=%v)\n", startURL, m.outputDir, m.maxDepth, m.convert)
	if len(rej) > 0 {
		fmt.Printf("Reject: %s\n", strings.Join(rej, ","))
	}
	if len(excl) > 0 {
		fmt.Printf("Exclude dirs: %s\n", strings.Join(excl, ","))
	}

	robotsURL := parsed.Scheme + "://" + parsed.Host + "/robots.txt"
	{
		rc := &http.Client{Timeout: 10 * time.Second}
		resp, err := rc.Get(robotsURL)
		if err != nil {
			log.Printf("robots probe failed: %v", err)
		} else {
			resp.Body.Close()
			if resp.StatusCode >= 400 {
				fmt.Printf("HTTP ERROR response %s [%s]\n", resp.Status, robotsURL)
			} else {
				fmt.Printf("HTTP response %s [%s]\n", resp.Status, robotsURL)
			}
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < m.workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			m.workerLoop(id)
		}(i + 1)
	}

	m.enqueue(startURL, 0)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			log.Printf("mirror canceled: %v", m.ctx.Err())
			m.closeTasks()
			wg.Wait()
			m.printStats()
			return nil
		case <-ticker.C:
			if m.maxPages > 0 && m.getDiscovered() >= m.maxPages {
				m.cancel()
				continue
			}
			if len(m.tasks) == 0 {
				time.Sleep(200 * time.Millisecond)
				if len(m.tasks) == 0 {
					m.closeTasks()
					wg.Wait()
					m.printStats()
					return nil
				}
			}
		}
	}
}

func (m *mirror) workerLoop(id int) {
	for t := range m.tasks {
		select {
		case <-m.ctx.Done():
			return
		default:
		}
		m.process(t, id)
	}
}

func (m *mirror) closeTasks() {
	if atomic.CompareAndSwapUint32(&m.tasksClosed, 0, 1) {
		close(m.tasks)
	}
}

func (m *mirror) enqueue(raw string, depth int) {
	if depth > m.maxDepth {
		m.incSkipped()
		return
	}
	if m.maxPages > 0 && m.getDiscovered() >= m.maxPages {
		return
	}
	u, err := url.Parse(raw)
	if err != nil {
		m.incSkipped()
		return
	}
	if !u.IsAbs() {
		u = m.baseURL.ResolveReference(u)
	}
	if !m.sameDomain(u) {
		m.incSkipped()
		return
	}
	key := u.String()
	if _, loaded := m.visited.LoadOrStore(key, true); loaded {
		m.incSkipped()
		return
	}
	fmt.Printf("Adding URL: %s\n", key)
	m.incDiscovered()
	if atomic.LoadUint32(&m.tasksClosed) == 1 {
		return
	}
	select {
	case m.tasks <- task{url: key, depth: depth}:
	case <-m.ctx.Done():
		return
	}
}

func (m *mirror) process(t task, workerID int) {
	reqCtx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", t.url, nil)
	if err != nil {
		m.incFailed()
		log.Printf("[%d] request create failed: %v", workerID, err)
		return
	}
	req.Header.Set("User-Agent", "Wget/1.0")

	resp, err := m.client.Do(req)
	if err != nil {
		m.incFailed()
		log.Printf("[%d] GET %s failed: %v", workerID, t.url, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		m.incFailed()
		log.Printf("[%d] GET %s status: %s", workerID, t.url, resp.Status)
		return
	}

	ct := resp.Header.Get("Content-Type")
	isHTML := strings.Contains(ct, "text/html") || looksLikeHTML(t.url)
	if isHTML {
		if parts := strings.Split(ct, "charset="); len(parts) > 1 {
			enc := strings.TrimSpace(parts[1])
			fmt.Printf("URI content encoding = '%s' (set by server response)\n", enc)
		}
	}

	localPath := m.localPathForURL(t.url, isHTML)
	if localPath == "" {
		m.incFailed()
		return
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		m.incFailed()
		log.Printf("[%d] mkdir failed: %v", workerID, err)
		return
	}

	fmt.Printf("Saving '%s'\n", localPath)
	fmt.Printf("HTTP response %s [%s]\n", resp.Status, t.url)
	f, err := os.Create(localPath)
	if err != nil {
		m.incFailed()
		log.Printf("[%d] create file failed: %v", workerID, err)
		return
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		m.incFailed()
		log.Printf("[%d] write failed: %v", workerID, err)
		return
	}
	f.Close()
	m.incDownloaded()

	isCSS := strings.Contains(ct, "text/css") || strings.HasSuffix(strings.ToLower(t.url), ".css")
	if isCSS {
		if err := m.parseCSSForURLs(localPath, t.url, t.depth+1); err != nil {
			log.Printf("[%d] parse css failed: %v", workerID, err)
		}
	}

	if m.convert {
		if isHTML {
			if err := m.rewriteLinks(localPath, t.url); err != nil {
				log.Printf("[%d] rewrite failed: %v", workerID, err)
			}
			fmt.Printf("convert %s %s %s\n\n", localPath, t.url, "utf-8")
		} else if isCSS {
			if err := m.rewriteCSS(localPath, t.url); err != nil {
				log.Printf("[%d] rewrite css failed: %v", workerID, err)
			}
		}
	}

	if isHTML {
		if err := m.extractAndEnqueueLinks(localPath, t.url, t.depth+1); err != nil {
			log.Printf("[%d] extract failed: %v", workerID, err)
		}
	}
}

func (m *mirror) extractAndEnqueueLinks(localPath, baseURL string, depth int) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				switch a.Key {
				case "href", "src", "action", "data-src", "data-href", "poster", "background":
					if link := strings.TrimSpace(a.Val); link != "" {
						if isSkippableLink(link) {
							continue
						}
						if resolved, err := resolveWithBase(link, baseURL); err == nil {
							if m.shouldReject(resolved) || m.isExcluded(resolved) {
								m.incSkipped()
								continue
							}
							m.enqueue(resolved, depth)
						}
					}
				case "srcset":
					for entry := range strings.SplitSeq(a.Val, ",") {
						parts := strings.Fields(strings.TrimSpace(entry))
						if len(parts) > 0 {
							if resolved, err := resolveWithBase(parts[0], baseURL); err == nil {
								if m.shouldReject(resolved) || m.isExcluded(resolved) {
									m.incSkipped()
									continue
								}
								m.enqueue(resolved, depth)
							}
						}
					}
				case "style":
					if a.Val != "" {
						matches := cssURLRe.FindAllStringSubmatch(a.Val, -1)
						for _, mm := range matches {
							if len(mm) < 2 {
								continue
							}
							link := strings.TrimSpace(mm[1])
							if link == "" || isSkippableLink(link) {
								continue
							}
							if resolved, err := resolveWithBase(link, baseURL); err == nil {
								if m.shouldReject(resolved) || m.isExcluded(resolved) {
									m.incSkipped()
									continue
								}
								m.enqueue(resolved, depth)
							}
						}
					}
				}
			}
		}
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "style") {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					matches := cssURLRe.FindAllStringSubmatch(c.Data, -1)
					for _, mm := range matches {
						if len(mm) < 2 {
							continue
						}
						link := strings.TrimSpace(mm[1])
						if link == "" || isSkippableLink(link) {
							continue
						}
						if resolved, err := resolveWithBase(link, baseURL); err == nil {
							if m.shouldReject(resolved) || m.isExcluded(resolved) {
								m.incSkipped()
								continue
							}
							m.enqueue(resolved, depth)
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
	return nil
}

var cssURLRe = regexp.MustCompile(`url\((?:"|')?(.*?)(?:"|')?\)`)

func (m *mirror) parseCSSForURLs(localPath, baseURL string, depth int) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	matches := cssURLRe.FindAllStringSubmatch(string(data), -1)
	for _, mm := range matches {
		if len(mm) < 2 {
			continue
		}
		link := strings.TrimSpace(mm[1])
		if link == "" || isSkippableLink(link) {
			continue
		}
		if resolved, err := resolveWithBase(link, baseURL); err == nil {
			if m.shouldReject(resolved) || m.isExcluded(resolved) {
				m.incSkipped()
				continue
			}
			m.enqueue(resolved, depth)
		}
	}
	return nil
}

func (m *mirror) rewriteCSS(localPath, baseURL string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(localPath)
	out := cssURLRe.ReplaceAllStringFunc(string(data), func(s string) string {
		mm := cssURLRe.FindStringSubmatch(s)
		if len(mm) < 2 {
			return s
		}
		link := strings.TrimSpace(mm[1])
		if link == "" || isSkippableLink(link) {
			return s
		}
		resolved, err := resolveWithBase(link, baseURL)
		if err != nil {
			return s
		}
		if !m.sameDomainString(resolved) {
			return s
		}
		tgt := m.localPathForURL(resolved, strings.HasSuffix(strings.ToLower(resolved), ".css") || looksLikeHTML(resolved))
		if tgt == "" {
			return s
		}
		if rel, err := filepath.Rel(dir, tgt); err == nil {
			return "url('" + filepath.ToSlash(rel) + "')"
		}
		return s
	})
	return os.WriteFile(localPath, []byte(out), 0644)
}

func (m *mirror) rewriteLinks(localPath, baseURL string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return err
	}

	htmlDir := filepath.Dir(localPath)
	var rewrite func(*html.Node)
	rewrite = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for i, a := range n.Attr {
				if a.Key == "href" || a.Key == "src" {
					val := a.Val
					if strings.HasPrefix(val, "//") {
						val = m.baseURL.Scheme + ":" + val
					}
					if strings.HasPrefix(val, "http://") || strings.HasPrefix(val, "https://") {
						if same := m.sameDomainString(val); same {
							tgtLocal := m.localPathForURL(val, looksLikeHTML(val))
							if tgtLocal != "" {
								if rel, err := filepath.Rel(htmlDir, tgtLocal); err == nil {
									n.Attr[i].Val = filepath.ToSlash(rel)
								}
							}
						}
					}
				}
				if a.Key == "style" && a.Val != "" {
					out := cssURLRe.ReplaceAllStringFunc(a.Val, func(s string) string {
						mm := cssURLRe.FindStringSubmatch(s)
						if len(mm) < 2 {
							return s
						}
						link := strings.TrimSpace(mm[1])
						if link == "" || isSkippableLink(link) {
							return s
						}
						resolved, err := resolveWithBase(link, baseURL)
						if err != nil || !m.sameDomainString(resolved) {
							return s
						}
						tgt := m.localPathForURL(resolved, looksLikeHTML(resolved))
						if tgt == "" {
							return s
						}
						if rel, err := filepath.Rel(htmlDir, tgt); err == nil {
							return "url('" + filepath.ToSlash(rel) + "')"
						}
						return s
					})
					n.Attr[i].Val = out
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			rewrite(c)
		}
	}
	rewrite(doc)

	var rewriteStyle func(*html.Node)
	rewriteStyle = func(n *html.Node) {
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "style") {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					out := cssURLRe.ReplaceAllStringFunc(c.Data, func(s string) string {
						mm := cssURLRe.FindStringSubmatch(s)
						if len(mm) < 2 {
							return s
						}
						link := strings.TrimSpace(mm[1])
						if link == "" || isSkippableLink(link) {
							return s
						}
						resolved, err := resolveWithBase(link, baseURL)
						if err != nil || !m.sameDomainString(resolved) {
							return s
						}
						tgt := m.localPathForURL(resolved, looksLikeHTML(resolved))
						if tgt == "" {
							return s
						}
						if rel, err := filepath.Rel(htmlDir, tgt); err == nil {
							return "url('" + filepath.ToSlash(rel) + "')"
						}
						return s
					})
					c.Data = out
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			rewriteStyle(c)
		}
	}
	rewriteStyle(doc)

	out, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer out.Close()
	return html.Render(out, doc)
}

func (m *mirror) localPathForURL(raw string, isHTML bool) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := u.Path
	if p == "" || p == "/" {
		p = "index.html"
	} else if strings.HasSuffix(p, "/") {
		p = strings.TrimPrefix(p, "/") + "/index.html"
	} else {
		p = strings.TrimPrefix(p, "/")
		if isHTML && !strings.Contains(filepath.Base(p), ".") {
			p = p + ".html"
		}
	}
	p = strings.Split(p, "?")[0]
	return filepath.Clean(filepath.Join(m.outputDir, filepath.FromSlash(p)))
}

func looksLikeHTML(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	p := u.Path
	return p == "" || strings.HasSuffix(p, "/") || strings.HasSuffix(p, ".html") || strings.HasSuffix(p, ".htm") || !strings.Contains(p, ".")
}

func (m *mirror) sameDomain(u *url.URL) bool {
	return strings.TrimPrefix(m.baseURL.Host, "www.") == strings.TrimPrefix(u.Host, "www.")
}

func (m *mirror) sameDomainString(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return m.sameDomain(u)
}

func (m *mirror) shouldReject(raw string) bool {
	if len(m.rejectExts) == 0 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	ext := strings.ToLower(filepath.Ext(strings.Split(u.Path, "?")[0]))
	for _, r := range m.rejectExts {
		if ext == r {
			return true
		}
	}
	return false
}

func (m *mirror) isExcluded(raw string) bool {
	if len(m.excludeDirs) == 0 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for _, ex := range m.excludeDirs {
		if slices.Contains(parts, ex) {
			return true
		}
	}
	return false
}

func resolveWithBase(link, base string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(link)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

func isSkippableLink(link string) bool {
	link = strings.TrimSpace(link)
	if link == "" || strings.HasPrefix(link, "#") || strings.HasPrefix(link, "javascript:") {
		return true
	}
	return false
}

func (m *mirror) incDiscovered() { m.statsMu.Lock(); m.stats.discovered++; m.statsMu.Unlock() }
func (m *mirror) incDownloaded() { m.statsMu.Lock(); m.stats.downloaded++; m.statsMu.Unlock() }
func (m *mirror) incFailed()     { m.statsMu.Lock(); m.stats.failed++; m.statsMu.Unlock() }
func (m *mirror) incSkipped()    { m.statsMu.Lock(); m.stats.skipped++; m.statsMu.Unlock() }
func (m *mirror) getDiscovered() int {
	m.statsMu.Lock()
	v := m.stats.discovered
	m.statsMu.Unlock()
	return v
}

func (m *mirror) printStats() {
	elapsed := time.Since(m.stats.start)
	fmt.Printf("\n=== Mirroring Statistics ===\n")
	fmt.Printf("Total URLs discovered: %d\n", m.stats.discovered)
	fmt.Printf("Successfully downloaded: %d\n", m.stats.downloaded)
	fmt.Printf("Failed downloads: %d\n", m.stats.failed)
	fmt.Printf("Skipped URLs: %d\n", m.stats.skipped)
	fmt.Printf("Time elapsed: %v\n", elapsed.Round(time.Second))
}
