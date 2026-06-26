package main

import (
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// urlExt returns the lower-case file extension (no leading dot) of the URL's
// path, or "" if there is none.
func urlExt(u *url.URL) string {
	return strings.ToLower(strings.TrimPrefix(path.Ext(u.Path), "."))
}

// destPath builds the on-disk destination for a downloaded file, mirroring its
// location on the site as a folder tree:
//
//	<out>/<root-domain>/<ext>/<file url path dirs...>/<filename>
//
// e.g. https://www.example.com/files/board/2026.pdf ->
//
//	<out>/example.com/pdf/files/board/2026.pdf
func (c *crawler) destPath(u *url.URL, ext string) string {
	clean := path.Clean("/" + u.Path) // normalize, collapse ../, leading slash
	dir, file := path.Split(clean)
	if file == "" {
		file = "index." + ext
	}

	segs := []string{c.cfg.outDir, c.root, ext}
	for _, s := range strings.Split(strings.Trim(dir, "/"), "/") {
		if s != "" {
			segs = append(segs, sanitizeSegment(s))
		}
	}
	segs = append(segs, sanitizeSegment(file))
	return filepath.Join(segs...)
}

// sanitizeSegment makes a URL path segment safe to use as a single filename
// component: it URL-decodes it, then replaces characters that are illegal or
// awkward on common filesystems.
func sanitizeSegment(s string) string {
	if dec, err := url.PathUnescape(s); err == nil {
		s = dec
	}
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			return '_'
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	switch s {
	case "", ".", "..":
		return "_"
	}
	return s
}

// exists reports whether a regular file already exists at path.
func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// saveToFile writes r to path, creating parent directories. It writes to a
// temporary file first and renames on success so an interrupted download never
// leaves a partial file that a later run would treat as complete.
func saveToFile(dest string, r io.Reader) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".webscour-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if the rename below succeeded

	n, err := io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return n, err
	}
	return n, nil
}
