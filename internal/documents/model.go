package documents

import (
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusProcessing    Status = "processing"
	StatusCompleted     Status = "completed"
	StatusOriginal      Status = "original"
	StatusDuplicate     Status = "duplicate"
	StatusPendingReview Status = "pending_review"
)

type Uniqueness string

const (
	Unique    Uniqueness = "unique"
	Original  Uniqueness = "original"
	Duplicate Uniqueness = "duplicate"
)

type DuplicateMatch struct {
	ID          uuid.UUID  `json:"id"`
	SourceID    uuid.UUID  `json:"source_id,omitempty"`
	DocumentID  uuid.UUID  `json:"document_id,omitempty"`
	Title       string     `json:"title"`
	ERP         string     `json:"erp"`
	Client      string     `json:"client,omitempty"`
	Member      string     `json:"member,omitempty"`
	Score       float64    `json:"score"`
	Uploaded    time.Time  `json:"uploaded_at"`
	FileURL     string     `json:"file_url,omitempty"`
	ContentType string     `json:"content_type,omitempty"`
	Kind        string     `json:"kind,omitempty"`
	Uniqueness  Uniqueness `json:"uniqueness,omitempty"`
}

type HashMatch struct {
	SourceID   uuid.UUID
	DocumentID uuid.UUID
	Title      string
	ERP        string
	Client     string
	Member     string
	Uniqueness Uniqueness
	Uploaded   time.Time
	Score      float64
	Kind       string
	PageCount  int
}

type PreviewMatch struct {
	ID         uuid.UUID  `json:"id"`
	Title      string     `json:"title"`
	ERP        string     `json:"erp"`
	Client     string     `json:"client"`
	Member     string     `json:"member"`
	Score      float64    `json:"score"`
	Uploaded   time.Time  `json:"uploaded_at"`
	Uniqueness Uniqueness `json:"uniqueness,omitempty"`
	Kind       string     `json:"kind,omitempty"`
}

type PreviewResult struct {
	OK         bool           `json:"ok"`
	Uniqueness Uniqueness     `json:"uniqueness"`
	Filename   string         `json:"filename,omitempty"`
	Digest     string         `json:"digest,omitempty"`
	Matches    []PreviewMatch `json:"matches"`
}

type Source struct {
	ID          uuid.UUID        `json:"id"`
	DocumentID  uuid.UUID        `json:"document_id"`
	Title       string           `json:"title"`
	StorageKey  string           `json:"-"`
	ContentType string           `json:"content_type"`
	SizeBytes   int64            `json:"size_bytes"`
	SHA256      *string          `json:"-"`
	Uniqueness  Uniqueness       `json:"uniqueness"`
	Score       *float64         `json:"score"`
	Uploaded    time.Time        `json:"uploaded_at"`
	FileURL     string           `json:"file_url"`
	Duplicates  []DuplicateMatch `json:"duplicates"`
	NeedsTitle  bool             `json:"-"`
	Released    bool             `json:"-"`
}

type Document struct {
	ID                uuid.UUID  `json:"id"`
	Title             string     `json:"title"`
	Uploader          string     `json:"uploader"`
	Client            string     `json:"client"`
	ERP               string     `json:"erp"`
	ANZSCO            string     `json:"anzsco"`
	Team              string     `json:"team"`
	Member            string     `json:"member"`
	Status            Status     `json:"status"`
	Uploaded          time.Time  `json:"uploaded_at"`
	URL               string     `json:"url"`
	FileURL           string     `json:"file_url"`
	Sources           []Source   `json:"sources"`
	OwnerID           *uuid.UUID `json:"owner_id,omitempty"`
	ReviewNote        string     `json:"review_note,omitempty"`
	ReviewRequestedAt *time.Time `json:"review_requested_at,omitempty"`
}

type CreateInput struct {
	Client string
	ERP    string
	ANZSCO string
	Team   string
	Member string
	Note   string
	Titles []string
	Files  []IncomingFile
}

type UploadDay struct {
	Day       string `json:"day"`
	Documents int    `json:"documents"`
	Sources   int    `json:"sources"`
}

type UploadStats struct {
	Bucket   string      `json:"bucket"`
	Timezone string      `json:"timezone"`
	From     string      `json:"from"`
	To       string      `json:"to"`
	Days     []UploadDay `json:"days"`
	Total    UploadTotal `json:"total"`
}

type UploadTotal struct {
	Documents int `json:"documents"`
	Sources   int `json:"sources"`
}

type IncomingFile struct {
	Name        string
	ContentType string
	Size        int64
	Open        func() (ReadSeekCloser, error)
}

type ReadSeekCloser interface {
	Read(p []byte) (int, error)
	Seek(offset int64, whence int) (int64, error)
	Close() error
}
