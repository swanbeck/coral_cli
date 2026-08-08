package ociarchive

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// whiteout markers defined by the OCI layer specification
const (
	whiteoutPrefix = ".wh."
	whiteoutOpaque = ".wh..wh..opq"
)

type Result struct {
	Files    int
	Dirs     int
	Symlinks int
	Bytes    int64
	Warnings []string
}

func (a *Archive) ExtractSubtree(img Image, subtree, dest string) (Result, error) {
	var res Result

	prefix := normalizeSubtree(subtree)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return res, fmt.Errorf("creating %s: %w", dest, err)
	}

	dirTimes := map[string]time.Time{}

	for i, layer := range img.layers {
		if foreignLayerMediaTypes[layer.MediaType] {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("layer %d (%s) is a foreign layer and is not held in the archive; skipped", i, layer.Digest))
			continue
		}
		if err := a.applyLayer(layer, prefix, dest, dirTimes, &res); err != nil {
			return res, fmt.Errorf("layer %d (%s): %w", i, layer.Digest, err)
		}
	}

	// deepest first, so a parent's timestamp is not disturbed by later work
	paths := make([]string, 0, len(dirTimes))
	for p := range dirTimes {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
	for _, p := range paths {
		_ = os.Chtimes(p, dirTimes[p], dirTimes[p])
	}

	return res, nil
}

// applyLayer writes one layer's contribution to the subtree over whatever earlier layers left in dest
func (a *Archive) applyLayer(desc Descriptor, prefix, dest string, dirTimes map[string]time.Time, res *Result) error {
	rc, err := a.openLayer(desc)
	if err != nil {
		return err
	}
	defer rc.Close()

	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading layer contents: %w", err)
		}

		name := normalizeMember(hdr.Name)

		// a whiteout of the subtree root itself sits in the parent directory, so it is not matched by the prefix test below and must be caught
		if prefix != "" && name == path.Join(path.Dir(prefix), whiteoutPrefix+path.Base(prefix)) {
			if err := removeContents(dest); err != nil {
				return err
			}
			clearPrefixed(dirTimes, dest)
			continue
		}

		rel, ok := relTo(name, prefix)
		if !ok {
			continue
		}

		base := path.Base(rel)
		switch {
		case rel == ".wh..wh..opq" || base == whiteoutOpaque:
			// opaque whiteout: everything the lower layers put in this directory is hidden, but entries from this same layer still apply
			dir, err := safeJoin(dest, path.Dir(rel))
			if err != nil {
				res.Warnings = append(res.Warnings, err.Error())
				continue
			}
			if err := removeContents(dir); err != nil {
				return err
			}
			clearPrefixed(dirTimes, dir)
			continue

		case strings.HasPrefix(base, whiteoutPrefix):
			target, err := safeJoin(dest, path.Join(path.Dir(rel), strings.TrimPrefix(base, whiteoutPrefix)))
			if err != nil {
				res.Warnings = append(res.Warnings, err.Error())
				continue
			}
			if err := os.RemoveAll(target); err != nil {
				return fmt.Errorf("applying whiteout %s: %w", name, err)
			}
			delete(dirTimes, target)
			continue
		}

		target, err := safeJoin(dest, rel)
		if err != nil {
			res.Warnings = append(res.Warnings, err.Error())
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode).Perm()); err != nil {
				return fmt.Errorf("creating directory %s: %w", target, err)
			}
			if err := os.Chmod(target, os.FileMode(hdr.Mode).Perm()); err != nil {
				return fmt.Errorf("setting mode on %s: %w", target, err)
			}
			dirTimes[target] = hdr.ModTime
			if rel != "" {
				res.Dirs++
			}

		case tar.TypeReg:
			n, err := writeFile(target, tr, hdr)
			if err != nil {
				return err
			}
			res.Files++
			res.Bytes += n

		case tar.TypeSymlink:
			// stored verbatim; resolving it here would break library soname chains that point outside the extracted subtree
			if err := replace(target); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("creating parent of %s: %w", target, err)
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("creating symlink %s: %w", target, err)
			}
			res.Symlinks++

		case tar.TypeLink:
			linkRel, ok := relTo(normalizeMember(hdr.Linkname), prefix)
			if !ok {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("%s is a hard link to %s, which lies outside the extracted subtree; skipped", name, hdr.Linkname))
				continue
			}
			source, err := safeJoin(dest, linkRel)
			if err != nil {
				res.Warnings = append(res.Warnings, err.Error())
				continue
			}
			if err := replace(target); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("creating parent of %s: %w", target, err)
			}
			if err := os.Link(source, target); err != nil {
				// a cross-device or unsupported link is recoverable: the point is to end up with the bytes, not to preserve the inode
				n, copyErr := copyFileFrom(source, target, hdr)
				if copyErr != nil {
					res.Warnings = append(res.Warnings,
						fmt.Sprintf("could not reproduce hard link %s -> %s: %v; skipped", name, hdr.Linkname, err))
					continue
				}
				res.Bytes += n
			}
			res.Files++

		default:
			// character devices, block devices, and FIFOs cannot appear in a library tree and would need privileges to create
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s has unsupported tar type %q; skipped", name, string(hdr.Typeflag)))
		}
	}
}

// openLayer streams a layer blob, transparently decompressing it
func (a *Archive) openLayer(desc Descriptor) (io.ReadCloser, error) {
	rc, err := a.openBlob(desc.Digest)
	if err != nil {
		return nil, err
	}

	br := bufio.NewReader(rc)
	magic, _ := br.Peek(4)

	switch {
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		zr, err := gzip.NewReader(br)
		if err != nil {
			rc.Close()
			return nil, fmt.Errorf("opening gzip layer %s: %w", desc.Digest, err)
		}
		return &layerReader{r: zr, closers: []io.Closer{zr, rc}}, nil

	case len(magic) >= 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
		rc.Close()
		return nil, fmt.Errorf("layer %s is zstd-compressed, which is not supported; rebuild the image with gzip layers (the buildx default) or re-save it with `skopeo copy --format oci`", desc.Digest)

	default:
		return &layerReader{r: br, closers: []io.Closer{rc}}, nil
	}
}

type layerReader struct {
	r       io.Reader
	closers []io.Closer
}

func (l *layerReader) Read(p []byte) (int, error) { return l.r.Read(p) }

func (l *layerReader) Close() error {
	var firstErr error
	for _, c := range l.closers {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// writeFile writes a regular file, replacing whatever a lower layer left there
func writeFile(target string, r io.Reader, hdr *tar.Header) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, fmt.Errorf("creating parent of %s: %w", target, err)
	}
	if err := replace(target); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, os.FileMode(hdr.Mode).Perm())
	if err != nil {
		return 0, fmt.Errorf("creating %s: %w", target, err)
	}
	n, err := io.Copy(f, r)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return n, fmt.Errorf("writing %s: %w", target, err)
	}
	if err := os.Chmod(target, os.FileMode(hdr.Mode).Perm()); err != nil {
		return n, fmt.Errorf("setting mode on %s: %w", target, err)
	}
	if err := os.Chtimes(target, hdr.ModTime, hdr.ModTime); err != nil {
		return n, fmt.Errorf("setting times on %s: %w", target, err)
	}
	return n, nil
}

// copyFileFrom duplicates an already-extracted file, used when a hard link cannot be created
func copyFileFrom(source, target string, hdr *tar.Header) (int64, error) {
	in, err := os.Open(source)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	return writeFile(target, in, hdr)
}

// replace clears any existing entry at target
func replace(target string) error {
	if _, err := os.Lstat(target); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("replacing %s: %w", target, err)
	}
	return nil
}

// removeContents empties a directory without removing the directory itself
func removeContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading %s: %w", dir, err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("clearing %s: %w", dir, err)
		}
	}
	return nil
}

// clearPrefixed drops recorded directory times beneath a path that was emptied, so a stale entry cannot resurrect a timestamp for a directory that is gone
func clearPrefixed(dirTimes map[string]time.Time, root string) {
	for p := range dirTimes {
		if p != root && strings.HasPrefix(p, root+string(os.PathSeparator)) {
			delete(dirTimes, p)
		}
	}
}

// normalizeSubtree turns a caller-supplied path such as "/opt/coral/lib" into the layout used inside a layer tar
func normalizeSubtree(subtree string) string {
	s := strings.Trim(subtree, "/")
	if s == "" || s == "." {
		return ""
	}
	return strings.TrimPrefix(path.Clean(s), "./")
}

// relTo reports whether name lies at or beneath prefix, and its path relative to it
func relTo(name, prefix string) (string, bool) {
	if prefix == "" {
		return name, true
	}
	if name == prefix {
		return "", true
	}
	if strings.HasPrefix(name, prefix+"/") {
		return name[len(prefix)+1:], true
	}
	return "", false
}

// safeJoin resolves rel beneath dest, refusing anything that would escape it
func safeJoin(dest, rel string) (string, error) {
	if rel == "" || rel == "." {
		return dest, nil
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+rel), "/")
	target := filepath.Join(dest, filepath.FromSlash(cleaned))
	if target != dest && !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
		return "", fmt.Errorf("entry %q would escape the output directory; skipped", rel)
	}
	return target, nil
}
