package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"coral_cli/internal/logging"
	"coral_cli/internal/ociarchive"
)

var (
	extractOutput    string
	extractPath      string
	extractEnvVar    string
	extractPlatforms []string
	extractLayout    string
	extractList      bool
)

const (
	defaultExtractEnv    = "CORAL_EXPORT_LIB"
	defaultExtractLayout = "{os}/{arch}/{variant}"
)

func init() {
	extractCmd.Flags().StringVarP(&extractOutput, "output", "o", "", "Output directory (default: the archive filename without its extension)")
	extractCmd.Flags().StringVar(&extractPath, "path", "", "Image path to extract (default: the value of CORAL_EXPORT_LIB in each image)")
	extractCmd.Flags().StringVar(&extractEnvVar, "env", defaultExtractEnv, "Environment variable naming the path to extract")
	extractCmd.Flags().StringSliceVar(&extractPlatforms, "platform", nil, "Only extract these platforms, e.g. linux/amd64,linux/arm64 (default: all)")
	extractCmd.Flags().StringVar(&extractLayout, "layout", defaultExtractLayout, "Template for each image's output subdirectory")
	extractCmd.Flags().BoolVar(&extractList, "list", false, "List the platforms in the archive without extracting anything")

	extractCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return []string{"tar"}, cobra.ShellCompDirectiveFilterFileExt
	}
}

var extractCmd = &cobra.Command{
	Use:   "extract <file>",
	Short: "Extract a library tree from every architecture in an OCI archive",
	Long: `Extract files from every image variant held in an OCI archive (.tar).

Unlike coral load, which installs only the variant matching the current platform,
extract reads all of them — including variants built for a foreign CPU
architecture. It reads the archive directly, so it needs neither a container
runtime nor skopeo, and the local daemon's refusal to hold a foreign-arch image
does not apply (nothing is ever executed, only read).

For each image, the path named by CORAL_EXPORT_LIB in that image's environment
is written to its own subdirectory of the output directory. The image's layers
are flattened in order with whiteouts applied, so the result matches what the
running container would see at that path. Modification times are preserved.

An extract.json file records, per variant, its platform, digests, config
labels, and the path that was extracted.

The output subdirectory is named by --layout, which accepts the tokens {os},
{arch}, {variant}, {distro} (the coral.ros_distro label), {version}, and {title}
(the org.opencontainers.image.* labels). Tokens that resolve to nothing are
dropped, so the default leaves an image with no CPU variant at linux/arm64
rather than linux/arm64/.`,
	Example: `  # every architecture, laid out as <os>/<arch>
  coral extract realsense-humble.tar -o ./staging

  # group by ROS distro instead, one architecture only
  coral extract realsense-humble.tar --layout '{distro}/{arch}' --platform linux/arm64

  # see what is in the archive
  coral extract realsense-humble.tar --list`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return extract(args[0])
	},
}

// one image variant's entry in the extract.json sidecar
type extractedImage struct {
	Dir            string              `json:"dir"`
	Platform       ociarchive.Platform `json:"platform"`
	ManifestDigest string              `json:"manifest_digest"`
	ConfigDigest   string              `json:"config_digest"`
	SourcePath     string              `json:"source_path"`
	Labels         map[string]string   `json:"labels,omitempty"`
	Files          int                 `json:"files"`
	Symlinks       int                 `json:"symlinks"`
	Bytes          int64               `json:"bytes"`
}

type extractManifest struct {
	Archive     string           `json:"archive"`
	ExtractedAt string           `json:"extracted_at"`
	SourceEnv   string           `json:"source_env,omitempty"`
	Images      []extractedImage `json:"images"`
}

func extract(archivePath string) error {
	archive, err := ociarchive.Open(archivePath)
	if err != nil {
		return err
	}

	images, err := archive.Images()
	if err != nil {
		return err
	}

	selected, err := selectPlatforms(images, extractPlatforms)
	if err != nil {
		return err
	}

	if extractList {
		for _, img := range selected {
			source := img.Env[extractEnvVar]
			if source == "" {
				source = logging.Yellow(fmt.Sprintf("no %s", extractEnvVar))
			}
			fmt.Printf("  %-20s %s\n", logging.BoldMagenta(img.Platform.String()), source)
		}
		return nil
	}

	output := extractOutput
	if output == "" {
		base := filepath.Base(archivePath)
		output = strings.TrimSuffix(base, filepath.Ext(base))
	}

	dirs, err := resolveDirs(selected, extractLayout)
	if err != nil {
		return err
	}

	fmt.Println(logging.Info(fmt.Sprintf("Extracting %d platform(s) from %s → %s",
		len(selected), archivePath, output)))

	result := extractManifest{
		Archive:     filepath.Base(archivePath),
		ExtractedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if extractPath == "" {
		result.SourceEnv = extractEnvVar
	}

	var skipped []string
	for i, img := range selected {
		source := extractPath
		if source == "" {
			source = img.Env[extractEnvVar]
		}
		if source == "" {
			skipped = append(skipped, fmt.Sprintf("%s does not set %s", img.Platform, extractEnvVar))
			continue
		}

		dest := filepath.Join(output, filepath.FromSlash(dirs[i]))
		// clear only this variant's directory, so several archives may share an output root without one stale run polluting the next
		if err := os.RemoveAll(dest); err != nil {
			return fmt.Errorf("clearing %s: %w", dest, err)
		}

		res, err := archive.ExtractSubtree(img, source, dest)
		if err != nil {
			return fmt.Errorf("extracting %s: %w", img.Platform, err)
		}
		for _, w := range res.Warnings {
			fmt.Println(logging.Warning(fmt.Sprintf("%s: %s", img.Platform, w)))
		}
		if res.Files == 0 && res.Symlinks == 0 {
			fmt.Println(logging.Warning(fmt.Sprintf(
				"%s: %s is empty in this image", img.Platform, source)))
		}

		fmt.Printf("  %-20s %s  (%d files, %d symlinks, %s)\n",
			logging.BoldMagenta(img.Platform.String()), dirs[i],
			res.Files, res.Symlinks, humanBytes(res.Bytes))

		result.Images = append(result.Images, extractedImage{
			Dir:            dirs[i],
			Platform:       img.Platform,
			ManifestDigest: img.ManifestDigest,
			ConfigDigest:   img.ConfigDigest,
			SourcePath:     source,
			Labels:         img.Labels,
			Files:          res.Files,
			Symlinks:       res.Symlinks,
			Bytes:          res.Bytes,
		})
	}

	for _, s := range skipped {
		fmt.Println(logging.Warning("Skipped: " + s))
	}
	if len(result.Images) == 0 {
		return fmt.Errorf("nothing extracted: no image in %s sets %s; pass --path to extract a fixed path instead",
			archivePath, extractEnvVar)
	}

	sidecar := filepath.Join(output, "extract.json")
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("serializing extract.json: %w", err)
	}
	if err := os.WriteFile(sidecar, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", sidecar, err)
	}

	fmt.Println(logging.Success(fmt.Sprintf("Extracted %d platform(s) to %s", len(result.Images), output)))
	return nil
}

// selectPlatforms filters images by the --platform values, which may be given as "linux/amd64", "amd64", or "linux/arm64/v8"
func selectPlatforms(images []ociarchive.Image, wanted []string) ([]ociarchive.Image, error) {
	if len(wanted) == 0 {
		return images, nil
	}

	var selected []ociarchive.Image
	matched := make(map[string]bool, len(wanted))
	for _, img := range images {
		for _, w := range wanted {
			if platformMatches(img.Platform, w) {
				selected = append(selected, img)
				matched[w] = true
				break
			}
		}
	}

	var missing []string
	for _, w := range wanted {
		if !matched[w] {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		var available []string
		for _, img := range images {
			available = append(available, img.Platform.String())
		}
		return nil, fmt.Errorf("no image for platform %s; the archive holds: %s",
			strings.Join(missing, ", "), strings.Join(available, ", "))
	}
	return selected, nil
}

func platformMatches(p ociarchive.Platform, want string) bool {
	switch parts := strings.Split(want, "/"); len(parts) {
	case 1:
		return p.Architecture == parts[0]
	case 2:
		return p.OS == parts[0] && p.Architecture == parts[1]
	case 3:
		return p.OS == parts[0] && p.Architecture == parts[1] && p.Variant == parts[2]
	default:
		return false
	}
}

// resolveDirs renders the layout template for each image and rejects a template that cannot tell two variants apart
func resolveDirs(images []ociarchive.Image, layout string) ([]string, error) {
	dirs := make([]string, len(images))
	claimed := map[string]ociarchive.Platform{}

	for i, img := range images {
		dir, err := renderLayout(layout, img)
		if err != nil {
			return nil, err
		}
		if prior, taken := claimed[dir]; taken {
			return nil, fmt.Errorf("layout %q puts both %s and %s in %q; choose a layout that distinguishes them",
				layout, prior, img.Platform, dir)
		}
		claimed[dir] = img.Platform
		dirs[i] = dir
	}
	return dirs, nil
}

// renderLayout substitutes the layout tokens for one image. Segments that resolve to nothing are dropped rather than left empty
func renderLayout(layout string, img ociarchive.Image) (string, error) {
	values := map[string]string{
		"os":      img.Platform.OS,
		"arch":    img.Platform.Architecture,
		"variant": img.Platform.Variant,
		"distro":  img.Labels["coral.ros_distro"],
		"version": img.Labels["org.opencontainers.image.version"],
		"title":   img.Labels["org.opencontainers.image.title"],
	}

	var rendered []string
	for _, segment := range strings.Split(layout, "/") {
		out := segment
		for {
			open := strings.Index(out, "{")
			if open < 0 {
				break
			}
			close := strings.Index(out[open:], "}")
			if close < 0 {
				return "", fmt.Errorf("layout %q has an unclosed '{'", layout)
			}
			close += open
			token := out[open+1 : close]
			value, known := values[token]
			if !known {
				return "", fmt.Errorf("layout %q uses unknown token {%s}; valid tokens are {os}, {arch}, {variant}, {distro}, {version}, {title}", layout, token)
			}
			out = out[:open] + value + out[close+1:]
		}
		out = sanitizeSegment(out)
		if out != "" {
			rendered = append(rendered, out)
		}
	}

	if len(rendered) == 0 {
		return "", fmt.Errorf("layout %q resolved to an empty path for %s; every token was empty", layout, img.Platform)
	}
	return strings.Join(rendered, "/"), nil
}

// sanitizeSegment keeps a rendered token from introducing path structure of its own; label values are author-supplied and may hold slashes or colons
func sanitizeSegment(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', 0:
			return '-'
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "." || s == ".." {
		return ""
	}
	return s
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
