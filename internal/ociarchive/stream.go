package ociarchive

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	goruntime "runtime"
	"sort"
	"strings"
)

const (
	nameAnnotation    = "io.containerd.image.name"
	refNameAnnotation = "org.opencontainers.image.ref.name"
)

// tagOf returns the tag portion of an image reference, or "" if it carries none
func tagOf(name string) string {
	i := strings.LastIndex(name, ":")
	if i < 0 || strings.Contains(name[i+1:], "/") {
		return ""
	}
	return name[i+1:]
}

// normalizeRef expands a familiar reference such as "coral-realsense:2.1.2" into the fully qualified form a containerd-backed store keys images by
func normalizeRef(name string) string {
	domain, remainder := "", name
	if i := strings.Index(name, "/"); i >= 0 {
		if head := name[:i]; head == "localhost" || strings.ContainsAny(head, ".:") {
			domain, remainder = head, name[i+1:]
		}
	}
	if domain == "" {
		domain = "docker.io"
	}
	if domain == "docker.io" && !strings.Contains(remainder, "/") {
		remainder = "library/" + remainder
	}
	if tagOf(remainder) == "" {
		remainder += ":latest"
	}
	return domain + "/" + remainder
}

// members at or below this size are buffered during the indexing pass; indexes, manifests, and configs are kilobytes, so this catches all of them without pulling in layers
const maxCachedMember = 256 << 10

// memberLoc is where a tar member's contents begin and how many bytes they run for
type memberLoc struct {
	offset int64
	size   int64
}

// OpenIndexed opens an OCI archive and walks its tar headers once, recording the location and size of each member and buffering small members in memory
func OpenIndexed(path string) (*Archive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening archive: %w", err)
	}
	defer f.Close()

	a := &Archive{
		path:    path,
		members: map[string]memberLoc{},
		cached:  map[string][]byte{},
	}

	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// tar.Reader reads whole 512-byte blocks straight from f with no buffering of its own, so the file offset here is exactly where this member's contents start
		off, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, fmt.Errorf("locating %s in %s: %w", hdr.Name, path, err)
		}
		name := normalizeMember(hdr.Name)
		a.members[name] = memberLoc{offset: off, size: hdr.Size}
		if hdr.Size <= maxCachedMember {
			buf, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("reading %s from %s: %w", hdr.Name, path, err)
			}
			a.cached[name] = buf
		}
	}

	if _, ok := a.members["index.json"]; !ok {
		if _, isDocker := a.members["manifest.json"]; isDocker {
			return nil, fmt.Errorf("%s is a docker-archive, not an OCI archive: it holds only one platform; rebuild it with `coral save`", path)
		}
		return nil, fmt.Errorf("%s does not look like an OCI archive (no index.json)", path)
	}
	return a, nil
}

// openIndexedMember serves a member from the indexing pass: from the buffer if it was small enough to keep, otherwise by seeking directly to its contents
func (a *Archive) openIndexedMember(name string) (io.ReadCloser, error) {
	if buf, ok := a.cached[name]; ok {
		return io.NopCloser(bytes.NewReader(buf)), nil
	}
	loc, ok := a.members[name]
	if !ok {
		return nil, fmt.Errorf("%q not found in archive", name)
	}
	f, err := os.Open(a.path)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(loc.offset, io.SeekStart); err != nil {
		f.Close()
		return nil, fmt.Errorf("seeking to %q in %s: %w", name, a.path, err)
	}
	return &memberReader{r: io.LimitReader(f, loc.size), f: f}, nil
}

// SelectHostPlatform returns the variant built for the platform this process is running on
func (a *Archive) SelectHostPlatform() (Image, error) {
	images, err := a.Images()
	if err != nil {
		return Image{}, err
	}

	var matches []Image
	held := make([]string, 0, len(images))
	for _, img := range images {
		held = append(held, img.Platform.String())
		if img.Platform.OS == goruntime.GOOS && img.Platform.Architecture == goruntime.GOARCH {
			matches = append(matches, img)
		}
	}
	if len(matches) == 0 {
		return Image{}, fmt.Errorf("%s holds no image for %s/%s (it holds: %s)",
			a.path, goruntime.GOOS, goruntime.GOARCH, strings.Join(held, ", "))
	}
	// several variants of one architecture (arm64/v8 alongside a bare arm64, say) are all runnable here; the unqualified one is the safer default
	for _, img := range matches {
		if img.Platform.Variant == "" {
			return img, nil
		}
	}
	return matches[0], nil
}

// NameFromLabels derives "name:tag" from the image's own OCI labels, which is what coral save writes
func (i Image) NameFromLabels() (string, error) {
	name := i.Labels["org.opencontainers.image.title"]
	version := i.Labels["org.opencontainers.image.version"]
	if name == "" || version == "" {
		return "", fmt.Errorf("image is missing required labels (org.opencontainers.image.title=%q, org.opencontainers.image.version=%q); use --name to specify the target image:tag", name, version)
	}
	return name + ":" + version, nil
}

// index as written into the streamed archive
type outIndex struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Manifests     []Descriptor `json:"manifests"`
}

// the legacy docker-archive descriptor, which is what a runtime storing images outside containerd reads instead of index.json
type outManifest struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

// StreamSinglePlatform writes a well-formed archive holding img alone, named name, to w
func (a *Archive) StreamSinglePlatform(w io.Writer, img Image, name string) error {
	if a.members == nil {
		return fmt.Errorf("StreamSinglePlatform requires an archive opened with OpenIndexed")
	}

	desc := img.desc
	desc.Platform = &Platform{
		OS:           img.Platform.OS,
		Architecture: img.Platform.Architecture,
		Variant:      img.Platform.Variant,
	}
	desc.Annotations = map[string]string{nameAnnotation: normalizeRef(name)}
	if tag := tagOf(name); tag != "" {
		desc.Annotations[refNameAnnotation] = tag
	}

	idx, err := json.Marshal(outIndex{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.index.v1+json",
		Manifests:     []Descriptor{desc},
	})
	if err != nil {
		return fmt.Errorf("building index.json: %w", err)
	}

	blobPath := func(d Descriptor) (string, error) {
		m, err := blobMember(d.Digest)
		if err != nil {
			return "", err
		}
		if _, ok := a.members[m]; !ok {
			if foreignLayerMediaTypes[d.MediaType] {
				return "", fmt.Errorf("layer %s is a foreign layer and is not held in %s; the image cannot be loaded from this archive alone", d.Digest, a.path)
			}
			return "", fmt.Errorf("%s references %s, which is not in the archive", name, d.Digest)
		}
		return m, nil
	}

	configMember, err := blobPath(Descriptor{Digest: img.ConfigDigest})
	if err != nil {
		return err
	}
	layerMembers := make([]string, 0, len(img.layers))
	for _, layer := range img.layers {
		m, err := blobPath(layer)
		if err != nil {
			return err
		}
		layerMembers = append(layerMembers, m)
	}
	manifestMember, err := blobPath(img.desc)
	if err != nil {
		return err
	}

	legacy, err := json.Marshal([]outManifest{{
		Config:   configMember,
		RepoTags: []string{name},
		Layers:   layerMembers,
	}})
	if err != nil {
		return fmt.Errorf("building manifest.json: %w", err)
	}

	// deduped and re-ordered by position in the source, so the copy below is a single forward sweep over the file
	members := make([]string, 0, len(layerMembers)+2)
	seen := map[string]bool{}
	for _, m := range append([]string{manifestMember, configMember}, layerMembers...) {
		if seen[m] {
			continue
		}
		seen[m] = true
		members = append(members, m)
	}
	sort.Slice(members, func(i, j int) bool {
		return a.members[members[i]].offset < a.members[members[j]].offset
	})

	tw := tar.NewWriter(w)

	f, err := os.Open(a.path)
	if err != nil {
		return fmt.Errorf("opening archive: %w", err)
	}
	defer f.Close()

	for _, member := range members {
		loc := a.members[member]
		// the recorded size is what is actually there; a descriptor that disagrees would produce a truncated blob and a digest mismatch downstream
		if err := tw.WriteHeader(&tar.Header{
			Name:     member,
			Mode:     0o644,
			Size:     loc.size,
			Typeflag: tar.TypeReg,
		}); err != nil {
			return fmt.Errorf("writing header for %s: %w", member, err)
		}
		if buf, ok := a.cached[member]; ok {
			if _, err := tw.Write(buf); err != nil {
				return fmt.Errorf("writing %s: %w", member, err)
			}
			continue
		}
		if _, err := f.Seek(loc.offset, io.SeekStart); err != nil {
			return fmt.Errorf("seeking to %s: %w", member, err)
		}
		if _, err := io.CopyN(tw, f, loc.size); err != nil {
			return fmt.Errorf("copying %s: %w", member, err)
		}
	}

	// metadata last, as `docker save` writes it
	if err := writeMember(tw, "index.json", idx); err != nil {
		return err
	}
	if err := writeMember(tw, "manifest.json", legacy); err != nil {
		return err
	}
	if err := writeMember(tw, "oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)); err != nil {
		return err
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("finishing archive: %w", err)
	}
	return nil
}

func writeMember(tw *tar.Writer, name string, content []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return fmt.Errorf("writing header for %s: %w", name, err)
	}
	if _, err := tw.Write(content); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	return nil
}
