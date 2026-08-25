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
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	MaxTitleBytes    = 50 << 20
	maxTitleResponse = 32 << 20
	TitleTimeout     = 120 * time.Second
	UntitledDocument = "Untitled document"
	UnreadableTitle  = "Title not readable (scanned PDF)"
	noOCRMessage     = "No OCR"
	titleOnlyQuery   = "/v1/ocr?title_only=1"
)

type Result struct {
	OK          bool   `json:"ok"`
	Title       string `json:"title,omitempty"`
	TitleSource string `json:"title_source,omitempty"`
	Filename    string `json:"filename,omitempty"`
	Message     string `json:"message,omitempty"`
	Method      string `json:"method,omitempty"`
}

type extractBody struct {
	OK          bool    `json:"ok"`
	Title       *string `json:"title"`
	TitleSource *string `json:"title_source"`
	Filename    *string `json:"filename"`
	Message     *string `json:"message"`
	Method      *string `json:"method"`
}

type Client struct {
	base     string
	http     *http.Client
	sem      chan struct{}
	fast     chan struct{}
	fastLive atomic.Int32
	log      *slog.Logger
	mu       sync.Mutex
	cached   string
	cachedAt time.Time
}

func New(base string, log *slog.Logger, limit, interactive int) *Client {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if limit < 1 {
		limit = 1
	}
	if interactive < 1 {
		interactive = 2
	}
	return &Client{
		base: base,
		http: &http.Client{Timeout: 0},
		sem:  make(chan struct{}, limit),
		fast: make(chan struct{}, interactive),
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
	return c.title(ctx, filename, r, c.sem)
}

// TitleNow is the form path: it never waits on background title workers.
func (c *Client) TitleNow(ctx context.Context, filename string, r io.Reader) (Result, error) {
	return c.title(ctx, filename, r, c.fast)
}

func (c *Client) InteractiveBusy() bool {
	return c != nil && c.fastLive.Load() > 0
}

func (c *Client) yieldForInteractive(ctx context.Context) error {
	for c.fastLive.Load() > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(40 * time.Millisecond):
		}
	}
	return nil
}

func (c *Client) title(ctx context.Context, filename string, r io.Reader, gate chan struct{}) (Result, error) {
	var out Result
	if !c.Configured() {
		return out, fmt.Errorf("engine is not configured")
	}
	if gate == nil {
		gate = c.sem
	}
	quick := gate == c.fast
	if quick {
		c.fastLive.Add(1)
		defer c.fastLive.Add(-1)
	}
	var payload bytes.Buffer
	payload.Grow(MaxTitleBytes + 64)
	if _, err := io.Copy(&payload, io.LimitReader(r, MaxTitleBytes)); err != nil {
		return out, err
	}
	if !quick {
		if err := c.yieldForInteractive(ctx); err != nil {
			return out, err
		}
	}
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return out, ctx.Err()
	}

	src := bytes.NewReader(payload.Bytes())
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if !quick {
			if err := c.yieldForInteractive(ctx); err != nil {
				return out, err
			}
		}
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return out, err
		}
		res, retryAfter, err := c.roundTrip(ctx, filename, src, quick)
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

func (c *Client) roundTrip(ctx context.Context, filename string, r io.Reader, quick bool) (Result, time.Duration, error) {
	var out Result
	var buf bytes.Buffer
	buf.Grow(MaxTitleBytes + 4096)
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

	reqCtx, cancel := context.WithTimeout(ctx, TitleTimeout)
	defer cancel()
	// title_only keeps the JSON to the heading fields. The full extract
	// payload includes every page of text, which used to blow past the 4MiB
	// read cap so json.Unmarshal failed after the engine had already titled
	// the PDF. The form then sat on "Generating title…" while a direct
	// engine call showed the heading immediately.
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.base+titleOnlyQuery, &buf)
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTitleResponse+1))
	if err != nil {
		return out, 2 * time.Second, err
	}
	if len(body) > maxTitleResponse {
		return out, 0, fmt.Errorf("engine response too large")
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		wait := 60 * time.Second
		if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
			if n, err := strconv.Atoi(ra); err == nil && n > 0 {
				wait = time.Duration(n) * time.Second
			}
		}
		if quick {
			// Form titles must fail fast so the UI can retry. A 60s Retry-After
			// is what made "Generating title…" hang while a direct engine call
			// still returned in about a second.
			return out, 0, fmt.Errorf("engine rate limited")
		}
		return out, wait, fmt.Errorf("engine rate limited")
	}
	if resp.StatusCode >= 500 {
		return out, 2 * time.Second, fmt.Errorf("engine status %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		out.OK = false
		out.Message = parseEngineDetail(body, resp.StatusCode)
		return out, 0, nil
	}

	var parsed extractBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return out, 0, err
	}
	out.OK = parsed.OK
	if parsed.Title != nil {
		out.Title = PrintedTitle(*parsed.Title)
	}
	if parsed.TitleSource != nil {
		out.TitleSource = *parsed.TitleSource
	}
	if parsed.Filename != nil {
		out.Filename = *parsed.Filename
	}
	if parsed.Message != nil {
		out.Message = strings.TrimSpace(*parsed.Message)
	}
	if parsed.Method != nil {
		out.Method = strings.TrimSpace(*parsed.Method)
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

func stripPDFExt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 4 && strings.EqualFold(s[len(s)-4:], ".pdf") {
		return strings.TrimSpace(s[:len(s)-4])
	}
	return s
}

func PrintedTitle(s string) string {
	out := SanitizeTitle(stripPDFExt(s))
	if out == "" {
		return ""
	}
	if looksLikeFilename(out) {
		return ""
	}
	if strings.EqualFold(out, UntitledDocument) {
		return ""
	}
	if strings.EqualFold(out, UnreadableTitle) {
		return ""
	}
	return out
}

// DisplayName is the Engine contract heading. Never the upload filename.
func DisplayName(res Result) string {
	if title := PrintedTitle(res.Title); title != "" {
		return title
	}
	if !res.OK && strings.EqualFold(strings.TrimSpace(res.Message), noOCRMessage) {
		return UnreadableTitle
	}
	return UntitledDocument
}

// TitleSettled is true when the engine result should stop retrying:
// a printed heading, or a scan that cannot be read.
func TitleSettled(name string) bool {
	if PrintedTitle(name) != "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(name), UnreadableTitle)
}

// IsPlaceholder is a title that must not be stored as final and must stay
// in the extraction queue: empty, Untitled, or a .pdf filename.
func IsPlaceholder(s string) bool {
	return !TitleSettled(s)
}

// PublicTitle is what the API and UI may show. Never a .pdf upload name.
func PublicTitle(s string) string {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, UnreadableTitle) {
		return UnreadableTitle
	}
	if title := PrintedTitle(s); title != "" {
		return title
	}
	return UntitledDocument
}

func looksLikeFilename(s string) bool {
	s = stripPDFExt(s)
	if s == "" {
		return true
	}
	// Paper headings have spaces. A single slug, with or without .pdf, is an
	// upload name (notes.pdf, 3.Our.ME_Project_Study_2) and must not be stored.
	return !strings.Contains(s, " ")
}

func parseEngineDetail(body []byte, status int) string {
	var payload struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &payload) == nil && len(payload.Detail) > 0 {
		var text string
		if json.Unmarshal(payload.Detail, &text) == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
		var items []struct {
			Msg string `json:"msg"`
		}
		if json.Unmarshal(payload.Detail, &items) == nil && len(items) > 0 && strings.TrimSpace(items[0].Msg) != "" {
			return strings.TrimSpace(items[0].Msg)
		}
	}
	return fmt.Sprintf("engine status %d", status)
}
