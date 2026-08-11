package ociarchive

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// media types that identify a manifest list (an index over per-platform manifests)
var indexMediaTypes = map[string]bool{
	"application/vnd.oci.image.index.v1+json":                   true,
	"application/vnd.docker.distribution.manifest.list.v2+json": true,
}

// media types that identify a single-platform image manifest
var manifestMediaTypes = map[string]bool{
	"application/vnd.oci.image.manifest.v1+json":           true,
	"application/vnd.docker.distribution.manifest.v2+json": true,
}

// blobs that describe a layer held elsewhere; they are not present in the archive
var foreignLayerMediaTypes = map[string]bool{
	"application/vnd.oci.image.layer.nondistributable.v1.tar":      true,
	"application/vnd.oci.image.layer.nondistributable.v1.tar+gzip": true,
	"application/vnd.docker.image.rootfs.foreign.diff.tar.gzip":    true,
}

// largest JSON blob (index, manifest, or config) that will be read into memory; these are kilobytes in practice, so the cap only guards against a malformed archive
const maxJSONBlob = 32 << 20

// Platform identifies the OS/CPU a particular image variant was built for
type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

// String renders the platform in the conventional "linux/arm64/v8" form
func (p Platform) String() string {
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// Descriptor is the subset of an OCI content descriptor this package needs
type Descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *Platform         `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// isAttestation reports whether a descriptor names a buildx attestation manifest
func (d Descriptor) isAttestation() bool {
	if d.Annotations["vnd.docker.reference.type"] == "attestation-manifest" {
		return true
	}
	return d.Platform != nil && isPlaceholderPlatform(*d.Platform)
}

// isPlaceholderPlatform reports whether a platform carries no usable identity: either buildx's literal "unknown/unknown" or nothing at all
func isPlaceholderPlatform(p Platform) bool {
	if p.OS == "unknown" || p.Architecture == "unknown" {
		return true
	}
	return p.OS == "" && p.Architecture == ""
}

type ociIndex struct {
	Manifests []Descriptor `json:"manifests"`
}

type ociManifest struct {
	Config Descriptor   `json:"config"`
	Layers []Descriptor `json:"layers"`
}

type ociConfig struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
	Config       struct {
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"config"`
}

// Image is one platform variant held in the archive, resolved far enough to extract from: its platform, its config metadata, and its ordered layer list
type Image struct {
	Platform       Platform
	ManifestDigest string
	ConfigDigest   string
	// labels from the image config, e.g. coral.ros_distro, coral.btcpp_version
	Labels map[string]string
	// env from the image config, split into key/value
	Env    map[string]string
	layers []Descriptor
}

// archive is a read-only handle on an oci-archive .tar file
type Archive struct {
	path string
}

// Open validates that path is an OCI archive and returns a handle on it
func Open(path string) (*Archive, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("opening archive: %w", err)
	}
	a := &Archive{path: path}
	if _, err := a.readMember("index.json"); err != nil {
		if _, dockerErr := a.readMember("manifest.json"); dockerErr == nil {
			return nil, fmt.Errorf("%s is a docker-archive, not an OCI archive: it holds only one platform; rebuild it with `coral save`", path)
		}
		return nil, fmt.Errorf("%s does not look like an OCI archive (no index.json): %w", path, err)
	}
	return a, nil
}

// Path returns the archive's filesystem path
func (a *Archive) Path() string { return a.path }

// Images returns every platform variant in the archive, in index order, descending through nested indexes
func (a *Archive) Images() ([]Image, error) {
	raw, err := a.readMember("index.json")
	if err != nil {
		return nil, err
	}
	var idx ociIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("parsing index.json: %w", err)
	}

	var images []Image
	seen := map[string]bool{}
	if err := a.collect(idx.Manifests, seen, &images); err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("archive %s holds no image manifests", a.path)
	}
	return images, nil
}

// collect walks a descriptor list, recursing into nested indexes and resolving each image manifest it reaches
func (a *Archive) collect(descs []Descriptor, seen map[string]bool, out *[]Image) error {
	for _, desc := range descs {
		if seen[desc.Digest] {
			continue
		}
		seen[desc.Digest] = true

		switch {
		case indexMediaTypes[desc.MediaType]:
			raw, err := a.readBlob(desc.Digest)
			if err != nil {
				return fmt.Errorf("reading nested index %s: %w", desc.Digest, err)
			}
			var nested ociIndex
			if err := json.Unmarshal(raw, &nested); err != nil {
				return fmt.Errorf("parsing nested index %s: %w", desc.Digest, err)
			}
			if err := a.collect(nested.Manifests, seen, out); err != nil {
				return err
			}

		case manifestMediaTypes[desc.MediaType]:
			if desc.isAttestation() {
				continue
			}
			img, err := a.resolveImage(desc)
			if err != nil {
				return err
			}
			// an attestation whose descriptor carried neither annotation nor platform is still identifiable once its config has been read
			if isPlaceholderPlatform(img.Platform) {
				continue
			}
			*out = append(*out, img)

		default:
			// signatures and other non-image entries live in the same index; skipping them by media type is the intended filter
			continue
		}
	}
	return nil
}

// resolveImage reads a manifest and its config into an Image
func (a *Archive) resolveImage(desc Descriptor) (Image, error) {
	raw, err := a.readBlob(desc.Digest)
	if err != nil {
		return Image{}, fmt.Errorf("reading manifest %s: %w", desc.Digest, err)
	}
	var mf ociManifest
	if err := json.Unmarshal(raw, &mf); err != nil {
		return Image{}, fmt.Errorf("parsing manifest %s: %w", desc.Digest, err)
	}

	rawCfg, err := a.readBlob(mf.Config.Digest)
	if err != nil {
		return Image{}, fmt.Errorf("reading config %s: %w", mf.Config.Digest, err)
	}
	var cfg ociConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return Image{}, fmt.Errorf("parsing config %s: %w", mf.Config.Digest, err)
	}

	plat := Platform{OS: cfg.OS, Architecture: cfg.Architecture, Variant: cfg.Variant}
	if desc.Platform != nil && desc.Platform.Architecture != "" {
		plat = *desc.Platform
	}

	env := make(map[string]string, len(cfg.Config.Env))
	for _, e := range cfg.Config.Env {
		if k, v, ok := strings.Cut(e, "="); ok {
			env[k] = v
		}
	}
	labels := cfg.Config.Labels
	if labels == nil {
		labels = map[string]string{}
	}

	return Image{
		Platform:       plat,
		ManifestDigest: desc.Digest,
		ConfigDigest:   mf.Config.Digest,
		Labels:         labels,
		Env:            env,
		layers:         mf.Layers,
	}, nil
}

// readBlob reads a content-addressed blob fully into memory
func (a *Archive) readBlob(digest string) ([]byte, error) {
	name, err := blobMember(digest)
	if err != nil {
		return nil, err
	}
	return a.readMember(name)
}

// openBlob streams a blob
func (a *Archive) openBlob(digest string) (io.ReadCloser, error) {
	name, err := blobMember(digest)
	if err != nil {
		return nil, err
	}
	return a.openMember(name)
}

func (a *Archive) readMember(name string) ([]byte, error) {
	rc, err := a.openMember(name)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxJSONBlob))
}

// openMember scans the archive for a named regular-file member and returns a reader positioned at its contents
func (a *Archive) openMember(name string) (io.ReadCloser, error) {
	f, err := os.Open(a.path)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			f.Close()
			return nil, fmt.Errorf("%q not found in archive", name)
		}
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("reading archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || normalizeMember(hdr.Name) != name {
			continue
		}
		return &memberReader{r: tr, f: f}, nil
	}
}

// memberReader ties a tar member's contents to the file handle backing it
type memberReader struct {
	r io.Reader
	f *os.File
}

func (m *memberReader) Read(p []byte) (int, error) { return m.r.Read(p) }
func (m *memberReader) Close() error               { return m.f.Close() }

// blobMember maps "sha256:abc..." to its path within the layout
func blobMember(digest string) (string, error) {
	alg, hex, ok := strings.Cut(digest, ":")
	if !ok || alg == "" || hex == "" {
		return "", fmt.Errorf("malformed digest %q", digest)
	}
	return path.Join("blobs", alg, hex), nil
}

// normalizeMember strips the "./" prefix some writers emit so member names compare equal to the layout-relative paths used here
func normalizeMember(name string) string {
	return strings.TrimPrefix(path.Clean(name), "./")
}
