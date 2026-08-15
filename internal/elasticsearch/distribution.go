package elasticsearch

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	releaseURL  = "https://artifacts.elastic.co/downloads/elasticsearch"
	snapshotURL = "https://storage.googleapis.com/kibana-ci-es-snapshots-daily"

	// BuildSnapshot follows the daily builds Kibana itself is tested against, which
	// is the only source that has a build for a development branch.
	BuildSnapshot = "snapshot"

	// BuildRelease uses the published artifacts, which exist only for versions that
	// have actually shipped.
	BuildRelease = "release"

	distributionTimeout = 30 * time.Minute
)

// distributionPlatforms maps the Go platform names onto the ones Elastic publishes.
var distributionPlatforms = map[string]string{
	"linux-amd64":  "linux-x86_64",
	"linux-arm64":  "linux-aarch64",
	"darwin-amd64": "darwin-x86_64",
	"darwin-arm64": "darwin-aarch64",
}

// distribution is one downloadable Elasticsearch archive.
type distribution struct {
	archiveURL  string
	checksumURL string
}

// EnsureDistribution reuses a verified Elasticsearch archive or downloads one into
// the runner cache, and returns the directory it was extracted to. The archive
// bundles its own JDK, so nothing outside the cache has to be installed.
func EnsureDistribution(
	ctx context.Context,
	runtimeDirectory string,
	version string,
	build string,
	reportProgress func(string),
) (string, error) {
	platform, supported := distributionPlatforms[runtime.GOOS+"-"+runtime.GOARCH]
	if !supported {
		return "", fmt.Errorf(
			"Elasticsearch is not published for %s-%s",
			runtime.GOOS,
			runtime.GOARCH,
		)
	}

	cacheDirectory := filepath.Join(
		runtimeDirectory,
		"tools",
		"elasticsearch",
		version+"-"+build,
	)
	installPath := filepath.Join(cacheDirectory, "dist")

	if isInstalledDistribution(installPath) {
		reportProgress(fmt.Sprintf("Reusing Elasticsearch %s %s", version, build))

		return installPath, nil
	}

	resolved, resolveError := resolveDistribution(ctx, version, build, platform)
	if resolveError != nil {
		return "", resolveError
	}

	reportProgress(fmt.Sprintf("Downloading Elasticsearch %s %s for %s", version, build, platform))

	archivePath, downloadError := downloadDistribution(ctx, cacheDirectory, resolved)
	if downloadError != nil {
		return "", downloadError
	}
	defer os.Remove(archivePath)

	reportProgress("Extracting Elasticsearch " + version)

	if installError := installDistribution(archivePath, cacheDirectory, installPath); installError != nil {
		return "", installError
	}

	return installPath, nil
}

// resolveDistribution turns a version into the archive and checksum to fetch.
func resolveDistribution(
	ctx context.Context,
	version string,
	build string,
	platform string,
) (distribution, error) {
	switch build {
	case BuildRelease:
		archiveURL := fmt.Sprintf(
			"%s/elasticsearch-%s-%s.tar.gz",
			releaseURL,
			version,
			platform,
		)

		return distribution{archiveURL: archiveURL, checksumURL: archiveURL + ".sha512"}, nil

	case BuildSnapshot:
		return resolveSnapshot(ctx, version, platform)

	default:
		return distribution{}, fmt.Errorf("unknown Elasticsearch build %q", build)
	}
}

// snapshotManifest is the part of the daily build manifest the runner reads.
type snapshotManifest struct {
	Archives []struct {
		Platform     string `json:"platform"`
		Architecture string `json:"architecture"`
		License      string `json:"license"`
		URL          string `json:"url"`
		Checksum     string `json:"checksum"`
	} `json:"archives"`
}

// resolveSnapshot reads the daily manifest, which names the build that was verified
// against Kibana rather than a fixed URL.
func resolveSnapshot(ctx context.Context, version string, platform string) (distribution, error) {
	manifestURL := fmt.Sprintf("%s/%s/manifest-latest-verified.json", snapshotURL, version)

	response, responseError := distributionRequest(ctx, manifestURL)
	if responseError != nil {
		return distribution{}, fmt.Errorf(
			"no Elasticsearch snapshot for %s: %w",
			version,
			responseError,
		)
	}
	defer response.Body.Close()

	var manifest snapshotManifest
	if decodeError := json.NewDecoder(response.Body).Decode(&manifest); decodeError != nil {
		return distribution{}, fmt.Errorf("read Elasticsearch snapshot manifest: %w", decodeError)
	}

	operatingSystem, architecture, _ := strings.Cut(platform, "-")

	for _, archive := range manifest.Archives {
		matches := archive.Platform == operatingSystem &&
			archive.Architecture == architecture &&
			archive.License == "default" &&
			strings.HasSuffix(archive.URL, ".tar.gz")

		if !matches {
			continue
		}

		return distribution{
			archiveURL:  archive.URL,
			checksumURL: pathSibling(archive.URL, archive.Checksum),
		}, nil
	}

	return distribution{}, fmt.Errorf(
		"Elasticsearch snapshot %s has no %s archive",
		version,
		platform,
	)
}

// pathSibling replaces the last element of a URL with another file name.
func pathSibling(url string, name string) string {
	base, _, found := strings.Cut(url, "?")
	if !found {
		base = url
	}

	return base[:strings.LastIndex(base, "/")+1] + path.Base(name)
}

// isInstalledDistribution reports whether a usable distribution is already cached.
func isInstalledDistribution(installPath string) bool {
	_, statError := os.Stat(filepath.Join(installPath, "bin", "elasticsearch"))

	return statError == nil
}

// downloadDistribution streams the archive to disk while hashing it, so an archive
// of several hundred megabytes never has to be held in memory, then compares the
// result with the published checksum.
func downloadDistribution(
	ctx context.Context,
	cacheDirectory string,
	resolved distribution,
) (string, error) {
	expectedChecksum, checksumError := fetchChecksum(ctx, resolved.checksumURL)
	if checksumError != nil {
		return "", checksumError
	}

	if createError := os.MkdirAll(cacheDirectory, 0o755); createError != nil {
		return "", fmt.Errorf("create Elasticsearch cache: %w", createError)
	}

	archiveFile, createError := os.CreateTemp(cacheDirectory, "elasticsearch.*.tar.gz")
	if createError != nil {
		return "", fmt.Errorf("create Elasticsearch download: %w", createError)
	}
	defer archiveFile.Close()

	digest := sha512.New()

	copyError := copyDistributionBody(ctx, resolved.archiveURL, archiveFile, digest)
	if copyError != nil {
		os.Remove(archiveFile.Name())

		return "", copyError
	}

	if actual := hex.EncodeToString(digest.Sum(nil)); actual != expectedChecksum {
		os.Remove(archiveFile.Name())

		return "", fmt.Errorf(
			"downloaded Elasticsearch archive does not match the published checksum",
		)
	}

	return archiveFile.Name(), nil
}

// copyDistributionBody writes the response to the file and the hash at once.
func copyDistributionBody(
	ctx context.Context,
	url string,
	archiveFile io.Writer,
	digest hash.Hash,
) error {
	response, responseError := distributionRequest(ctx, url)
	if responseError != nil {
		return responseError
	}
	defer response.Body.Close()

	if _, copyError := io.Copy(io.MultiWriter(archiveFile, digest), response.Body); copyError != nil {
		return fmt.Errorf("download Elasticsearch: %w", copyError)
	}

	return nil
}

// fetchChecksum reads a sidecar whose body is the hash followed by a file name.
func fetchChecksum(ctx context.Context, url string) (string, error) {
	response, responseError := distributionRequest(ctx, url)
	if responseError != nil {
		return "", responseError
	}
	defer response.Body.Close()

	body, readError := io.ReadAll(io.LimitReader(response.Body, 1024))
	if readError != nil {
		return "", fmt.Errorf("read Elasticsearch checksum: %w", readError)
	}

	checksum, _, found := strings.Cut(strings.TrimSpace(string(body)), " ")
	if !found || checksum == "" {
		return "", fmt.Errorf("Elasticsearch checksum at %s is not readable", url)
	}

	return checksum, nil
}

// distributionRequest performs one GET and rejects anything but a success, so an
// error page is never mistaken for content.
func distributionRequest(ctx context.Context, url string) (*http.Response, error) {
	request, requestError := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if requestError != nil {
		return nil, fmt.Errorf("create request for %s: %w", url, requestError)
	}

	client := &http.Client{Timeout: distributionTimeout}

	response, responseError := client.Do(request)
	if responseError != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, responseError)
	}

	if response.StatusCode != http.StatusOK {
		response.Body.Close()

		return nil, fmt.Errorf("fetch %s: HTTP %d", url, response.StatusCode)
	}

	return response, nil
}

// installDistribution extracts the archive and moves its single top-level directory
// to a fixed path, so callers do not depend on whether the archive was a release or
// a snapshot.
func installDistribution(archivePath string, cacheDirectory string, installPath string) error {
	staging, stagingError := os.MkdirTemp(cacheDirectory, "extract.*")
	if stagingError != nil {
		return fmt.Errorf("create Elasticsearch staging directory: %w", stagingError)
	}
	defer os.RemoveAll(staging)

	if extractError := extractDistribution(archivePath, staging); extractError != nil {
		return extractError
	}

	extracted, extractedError := extractedRoot(staging)
	if extractedError != nil {
		return extractedError
	}

	if _, statError := os.Stat(filepath.Join(extracted, "bin", "elasticsearch")); statError != nil {
		return fmt.Errorf("extracted Elasticsearch archive has no bin/elasticsearch")
	}

	os.RemoveAll(installPath)

	if renameError := os.Rename(extracted, installPath); renameError != nil {
		return fmt.Errorf("install Elasticsearch: %w", renameError)
	}

	return nil
}

// extractedRoot returns the single directory an Elasticsearch archive unpacks into.
func extractedRoot(staging string) (string, error) {
	entries, readError := os.ReadDir(staging)
	if readError != nil {
		return "", fmt.Errorf("read extracted Elasticsearch archive: %w", readError)
	}

	if len(entries) != 1 || !entries[0].IsDir() {
		return "", fmt.Errorf(
			"expected one directory in the Elasticsearch archive, found %d entries",
			len(entries),
		)
	}

	return filepath.Join(staging, entries[0].Name()), nil
}

// extractDistribution unpacks the archive, preserving the modes that make the
// launcher and the bundled JDK executable, and the symlinks the JDK relies on.
func extractDistribution(archivePath string, destination string) error {
	archiveFile, openError := os.Open(archivePath)
	if openError != nil {
		return fmt.Errorf("open Elasticsearch archive: %w", openError)
	}
	defer archiveFile.Close()

	decompressed, gzipError := gzip.NewReader(archiveFile)
	if gzipError != nil {
		return fmt.Errorf("read Elasticsearch archive: %w", gzipError)
	}
	defer decompressed.Close()

	reader := tar.NewReader(decompressed)

	for {
		header, nextError := reader.Next()
		if nextError == io.EOF {
			return nil
		}
		if nextError != nil {
			return fmt.Errorf("read Elasticsearch archive: %w", nextError)
		}

		if writeError := extractEntry(reader, header, destination); writeError != nil {
			return writeError
		}
	}
}

// extractEntry writes one archive member. Paths are resolved against the destination
// and rejected if they leave it, because an archive is remote input.
func extractEntry(reader io.Reader, header *tar.Header, destination string) error {
	target, targetError := resolveEntryPath(destination, header.Name)
	if targetError != nil {
		return targetError
	}

	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o755)

	case tar.TypeReg:
		return writeEntryFile(reader, target, os.FileMode(header.Mode).Perm())

	case tar.TypeSymlink:
		return writeEntryLink(header.Linkname, target)

	case tar.TypeLink:
		source, sourceError := resolveEntryPath(destination, header.Linkname)
		if sourceError != nil {
			return sourceError
		}

		return os.Link(source, target)

	default:
		return fmt.Errorf("unsupported entry %q in Elasticsearch archive", header.Name)
	}
}

// resolveEntryPath places an archive path inside the destination, refusing any that
// would land outside it.
func resolveEntryPath(destination string, name string) (string, error) {
	target := filepath.Join(destination, filepath.Clean("/"+name))

	if target != destination &&
		!strings.HasPrefix(target, destination+string(os.PathSeparator)) {
		return "", fmt.Errorf("Elasticsearch archive entry %q escapes the cache", name)
	}

	return target, nil
}

// writeEntryFile creates one file with the mode the archive recorded, which is what
// leaves bin/elasticsearch and the bundled JDK executable.
func writeEntryFile(reader io.Reader, target string, mode os.FileMode) error {
	if createError := os.MkdirAll(filepath.Dir(target), 0o755); createError != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(target), createError)
	}

	file, createError := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if createError != nil {
		return fmt.Errorf("create %s: %w", target, createError)
	}
	defer file.Close()

	if _, copyError := io.Copy(file, reader); copyError != nil {
		return fmt.Errorf("write %s: %w", target, copyError)
	}

	return file.Close()
}

// writeEntryLink recreates a symlink, which the bundled JDK relies on.
func writeEntryLink(linkName string, target string) error {
	if createError := os.MkdirAll(filepath.Dir(target), 0o755); createError != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(target), createError)
	}

	os.Remove(target)

	if linkError := os.Symlink(linkName, target); linkError != nil {
		return fmt.Errorf("link %s: %w", target, linkError)
	}

	return nil
}
