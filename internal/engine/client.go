package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Result struct {
	OK          bool   `json:"ok"`
	Title       string `json:"title,omitempty"`
	TitleSource string `json:"title_source,omitempty"`
	Filename    string `json:"filename,omitempty"`
}

type extractBody struct {
	OK          bool    `json:"ok"`
	Title       *string `json:"title"`
	TitleSource *string `json:"title_source"`
	Filename    *string `json:"filename"`
}

type Client struct {
	base     string
	http     *http.Client
	sem      chan struct{}
	log      *slog.Logger
	mu       sync.Mutex
	cached   string
	cachedAt time.Time
}

func New(base string, log *slog.Logger) *Client {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	return &Client{
		base: base,
		http: &http.Client{Timeout: 0},
		sem:  make(chan struct{}, 2),
		log:  log,
	}
}

func (c *Client) Configured() bool {
	return c != nil && c.base != ""
}

func (c *Client) Status() string {
	if !c.Configured() {
		return "off"
	}
	c.mu.Lock()
	if time.Since(c.cachedAt) < 5*time.Second && c.cached != "" {
		s := c.cached
		c.mu.Unlock()
		return s
	}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/health", nil)
	if err != nil {
		return c.store("down")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return c.store("down")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return c.store("ok")
	}
	return c.store("down")
}

func (c *Client) store(v string) string {
	c.mu.Lock()
	c.cached = v
	c.cachedAt = time.Now()
	c.mu.Unlock()
	return v
}

func (c *Client) Title(ctx context.Context, filename string, r io.Reader) (Result, error) {
	var out Result
	if !c.Configured() {
		return out, fmt.Errorf("engine is not configured")
	}
	var payload bytes.Buffer
	if _, err := io.Copy(&payload, io.LimitReader(r, 50<<20)); err != nil {
		return out, err
	}
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return out, ctx.Err()
	}

	src := bytes.NewReader(payload.Bytes())
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return out, err
		}
		res, retryAfter, err := c.roundTrip(ctx, filename, src)
		if err == nil {
			return res, nil
		}
		last = err
		if retryAfter <= 0 {
			return out, err
		}
		timer := time.NewTimer(retryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return out, ctx.Err()
		case <-timer.C:
		}
	}
	if last == nil {
		last = fmt.Errorf("engine title failed")
	}
	return out, last
}

func (c *Client) roundTrip(ctx context.Context, filename string, r io.Reader) (Result, time.Duration, error) {
	var out Result
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return out, 0, err
	}
	if _, err := io.Copy(part, r); err != nil {
		return out, 0, err
	}
	if err := mw.Close(); err != nil {
		return out, 0, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.base+"/v1/ocr", &buf)
	if err != nil {
		return out, 0, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Request-ID", uuid.NewString())

	resp, err := c.http.Do(req)
	if err != nil {
		return out, 2 * time.Second, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode == http.StatusTooManyRequests {
		wait := 60 * time.Second
		if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
			if n, err := strconv.Atoi(ra); err == nil && n > 0 {
				wait = time.Duration(n) * time.Second
			}
		}
		return out, wait, fmt.Errorf("engine rate limited")
	}
	if resp.StatusCode >= 500 {
		return out, 2 * time.Second, fmt.Errorf("engine status %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return out, 0, fmt.Errorf("engine status %d", resp.StatusCode)
	}

	var parsed extractBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return out, 0, err
	}
	out.OK = parsed.OK
	if parsed.Title != nil {
		out.Title = SanitizeTitle(*parsed.Title)
	}
	if parsed.TitleSource != nil {
		out.TitleSource = *parsed.TitleSource
	}
	if parsed.Filename != nil {
		out.Filename = *parsed.Filename
	}
	return out, 0, nil
}

func SanitizeTitle(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		if r < 32 {
			continue
		}
		b.WriteRune(r)
		prevSpace = r == ' '
	}
	out := strings.TrimSpace(b.String())
	runes := []rune(out)
	if len(runes) > 200 {
		out = string(runes[:200])
		out = strings.TrimSpace(out)
	}
	return out
}
