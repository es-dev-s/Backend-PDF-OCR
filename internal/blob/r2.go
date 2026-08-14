package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type R2 struct {
	bucket     string
	prefix     string
	client     *s3.Client
	uploader   *manager.Uploader
	cacheDir   string
	mu         sync.Mutex
	readyAt    time.Time
	readyErr   error
	cacheMu    sync.Mutex
	cacheLocks map[string]*sync.Mutex
}

func NewR2(opts R2Options) (*R2, error) {
	account := strings.TrimSpace(opts.AccountID)
	access := strings.TrimSpace(opts.AccessKey)
	secret := strings.TrimSpace(opts.Secret)
	bucket := strings.TrimSpace(opts.Bucket)
	if account == "" || access == "" || secret == "" || bucket == "" {
		return nil, fmt.Errorf("R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, and R2_BUCKET are required")
	}
	endpoint := strings.TrimRight(strings.TrimSpace(opts.Endpoint), "/")
	if endpoint == "" {
		endpoint = "https://" + account + ".r2.cloudflarestorage.com"
	}
	cacheDir := filepath.Join(os.TempDir(), "ocr-r2")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("r2 cache: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(access, secret, "")),
		awsconfig.WithRegion("auto"),
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
		awsconfig.WithHTTPClient(&http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          64,
				MaxIdleConnsPerHost:   16,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("r2 config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
		o.RetryMaxAttempts = 5
		o.RetryMode = aws.RetryModeStandard
	})
	return &R2{
		bucket: bucket,
		prefix: strings.Trim(strings.ReplaceAll(strings.TrimSpace(opts.Prefix), "\\", "/"), "/"),
		client: client,
		uploader: manager.NewUploader(client, func(u *manager.Uploader) {
			u.PartSize = 8 << 20
			u.Concurrency = 1
			u.LeavePartsOnError = false
		}),
		cacheDir:   cacheDir,
		cacheLocks: make(map[string]*sync.Mutex),
	}, nil
}

func (r *R2) lockCache(path string) func() {
	r.cacheMu.Lock()
	m := r.cacheLocks[path]
	if m == nil {
		m = &sync.Mutex{}
		r.cacheLocks[path] = m
	}
	r.cacheMu.Unlock()
	m.Lock()
	return m.Unlock
}

func (r *R2) Driver() string { return "r2" }

func (r *R2) Ready(ctx context.Context) error {
	r.mu.Lock()
	if time.Since(r.readyAt) < 5*time.Second {
		err := r.readyErr
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
	}
	_, err := r.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(r.bucket)})
	r.mu.Lock()
	r.readyAt = time.Now()
	r.readyErr = err
	r.mu.Unlock()
	if err != nil {
		return fmt.Errorf("r2 bucket: %w", err)
	}
	return nil
}

func (r *R2) Put(ctx context.Context, key string, reader io.Reader, _ int64, contentType string) error {
	obj, err := r.objectKey(key)
	if err != nil {
		return err
	}
	if contentType == "" {
		contentType = contentTypeFor(key)
	}
	cache := r.cachePath(obj)
	unlock := r.lockCache(cache)
	defer unlock()
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cache), ".r2-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
	}
	body := io.TeeReader(reader, tmp)
	filename := filepath.Base(key)
	_, err = r.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:             aws.String(r.bucket),
		Key:                aws.String(obj),
		Body:               body,
		ContentType:        aws.String(contentType),
		ContentDisposition: aws.String(fmt.Sprintf(`inline; filename=%q`, filename)),
		CacheControl:       aws.String("private, max-age=31536000, immutable"),
	})
	if err != nil {
		return fmt.Errorf("r2 put: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, cache); err != nil {
		return err
	}
	ok = true
	return nil
}

func (r *R2) Open(ctx context.Context, key string) (io.ReadCloser, int64, string, error) {
	obj, err := r.objectKey(key)
	if err != nil {
		return nil, 0, "", err
	}
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
	}
	out, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(obj),
	})
	if err != nil {
		return nil, 0, "", fmt.Errorf("r2 get: %w", err)
	}
	size := int64(0)
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	ctype := contentTypeFor(key)
	if out.ContentType != nil && strings.TrimSpace(*out.ContentType) != "" {
		ctype = *out.ContentType
	}
	return out.Body, size, ctype, nil
}

func (r *R2) LocalPath(ctx context.Context, key string) (string, error) {
	obj, err := r.objectKey(key)
	if err != nil {
		return "", err
	}
	cache := r.cachePath(obj)
	unlock := r.lockCache(cache)
	defer unlock()
	if info, err := os.Stat(cache); err == nil && info.Size() > 0 {
		return cache, nil
	}
	rc, _, _, err := r.Open(ctx, key)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cache), ".r2-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, rc); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, cache); err != nil {
		return "", err
	}
	ok = true
	return cache, nil
}

func (r *R2) Delete(ctx context.Context, key string) error {
	obj, err := r.objectKey(key)
	if err != nil {
		return err
	}
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	cache := r.cachePath(obj)
	unlock := r.lockCache(cache)
	defer unlock()
	_, err = r.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(obj),
	})
	_ = os.Remove(cache)
	if err != nil {
		return fmt.Errorf("r2 delete: %w", err)
	}
	return nil
}

func (r *R2) PurgeAll(ctx context.Context) (int, error) {
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	removed := 0
	var token *string
	for {
		out, err := r.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(r.bucket),
			ContinuationToken: token,
			MaxKeys:           aws.Int32(1000),
		})
		if err != nil {
			return removed, fmt.Errorf("r2 list: %w", err)
		}
		if len(out.Contents) == 0 {
			if aws.ToBool(out.IsTruncated) && out.NextContinuationToken != nil {
				token = out.NextContinuationToken
				continue
			}
			break
		}
		objs := make([]types.ObjectIdentifier, 0, len(out.Contents))
		for _, item := range out.Contents {
			if item.Key == nil || *item.Key == "" {
				continue
			}
			objs = append(objs, types.ObjectIdentifier{Key: item.Key})
			_ = os.Remove(r.cachePath(*item.Key))
		}
		if len(objs) > 0 {
			if _, err := r.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(r.bucket),
				Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)},
			}); err != nil {
				return removed, fmt.Errorf("r2 purge: %w", err)
			}
			removed += len(objs)
		}
		if !aws.ToBool(out.IsTruncated) || out.NextContinuationToken == nil {
			break
		}
		token = out.NextContinuationToken
	}
	return removed, nil
}

func (r *R2) objectKey(key string) (string, error) {
	clean, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	return joinPrefix(r.prefix, clean), nil
}

func (r *R2) cachePath(objKey string) string {
	sum := sha256.Sum256([]byte(objKey))
	ext := filepath.Ext(objKey)
	if len(ext) > 8 {
		ext = ""
	}
	return filepath.Join(r.cacheDir, hex.EncodeToString(sum[:])+ext)
}
