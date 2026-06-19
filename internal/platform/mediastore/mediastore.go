// Package mediastore persists user-uploaded media (post images/videos) and
// exposes a public URL path for each stored object.
//
// The local implementation writes to the filesystem and is served back by a
// static route on the API. It sits behind the Store interface so production can
// swap in an S3/MinIO-backed implementation (presigned delivery URLs) without
// touching callers — see internal/media/doc.go for the intended direction.
package mediastore

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Store persists an object under a slash-separated key and returns the public
// URL path at which it can later be fetched.
type Store interface {
	// Save writes r under key (e.g. "posts/<id>/image_1.jpg") and returns the
	// public URL path (e.g. "/media/posts/<id>/image_1.jpg").
	Save(key string, r io.Reader) (string, error)
	// RemoveAll deletes every object under prefix. Used to roll back a failed
	// post creation so orphaned files don't pile up.
	RemoveAll(prefix string) error
}

// Local is a filesystem-backed Store. Objects live under Root and are served
// under PublicPrefix (e.g. "/media").
type Local struct {
	root         string
	publicPrefix string
}

// NewLocal builds a filesystem store rooted at root, serving under publicPrefix.
// Directories are created lazily on Save, so no setup is required up front.
func NewLocal(root, publicPrefix string) *Local {
	return &Local{
		root:         root,
		publicPrefix: "/" + strings.Trim(publicPrefix, "/"),
	}
}

// Save writes the object and returns its public URL path.
func (l *Local) Save(key string, r io.Reader) (string, error) {
	clean := cleanKey(key)
	dst := filepath.Join(l.root, filepath.FromSlash(clean))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return "", err
	}
	return l.publicPrefix + "/" + clean, nil
}

// RemoveAll deletes everything under prefix (relative to root).
func (l *Local) RemoveAll(prefix string) error {
	clean := cleanKey(prefix)
	if clean == "" {
		return nil // never wipe the whole root
	}
	return os.RemoveAll(filepath.Join(l.root, filepath.FromSlash(clean)))
}

// cleanKey strips leading slashes and any "." / ".." segments so a key can
// never escape the storage root (path-traversal guard).
func cleanKey(key string) string {
	parts := strings.Split(strings.TrimLeft(key, "/"), "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, "/")
}
