package media

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Attachment struct {
	Kind string
	Path string
	Name string
	MIME string
}

type Store struct {
	Dir string
}

func NewStore(dir string) Store {
	return Store{Dir: dir}
}

func (s Store) SaveBytes(source string, name string, mimeType string, data []byte) (Attachment, error) {
	path, name, err := s.nextPath(source, name, mimeType)
	if err != nil {
		return Attachment{}, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return Attachment{}, err
	}
	return Attachment{Kind: kindFor(mimeType), Path: path, Name: name, MIME: mimeType}, nil
}

func (s Store) Download(ctx context.Context, source string, name string, mimeType string, url string) (Attachment, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Attachment{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Attachment{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Attachment{}, fmt.Errorf("download failed: %s", resp.Status)
	}
	if mimeType == "" {
		mimeType = resp.Header.Get("Content-Type")
	}
	path, name, err := s.nextPath(source, name, mimeType)
	if err != nil {
		return Attachment{}, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Attachment{}, err
	}
	defer file.Close()
	if _, err := io.Copy(file, resp.Body); err != nil {
		return Attachment{}, err
	}
	return Attachment{Kind: kindFor(mimeType), Path: path, Name: name, MIME: mimeType}, nil
}

func (s Store) nextPath(source string, name string, mimeType string) (string, string, error) {
	if name = sanitize(name); name == "" {
		name = time.Now().UTC().Format("20060102-150405.000000000")
	}
	if filepath.Ext(name) == "" {
		if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
			name += exts[0]
		}
	}
	dir := filepath.Join(s.Dir, sanitize(source))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		abs, err := filepath.Abs(path)
		return abs, name, err
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d%s", base, i, ext)
		path = filepath.Join(dir, candidate)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			abs, err := filepath.Abs(path)
			return abs, candidate, err
		}
	}
}

func kindFor(mimeType string) string {
	if strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return "image"
	}
	return "document"
}

func sanitize(value string) string {
	value = strings.TrimSpace(value)
	value = filepath.Base(value)
	value = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, value)
	value = strings.Trim(value, ".-")
	if len(value) > 180 {
		value = value[:180]
	}
	return value
}
