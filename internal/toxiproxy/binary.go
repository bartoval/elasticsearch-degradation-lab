package toxiproxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	toxiproxyVersion      = "2.12.0"
	toxiproxyReleaseURL   = "https://github.com/Shopify/toxiproxy/releases/download"
	binaryDownloadTimeout = 2 * time.Minute
)

type releaseAsset struct {
	filename string
	checksum string
}

var releaseAssets = map[string]releaseAsset{
	"linux-amd64": {
		filename: "toxiproxy-server-linux-amd64",
		checksum: "556d891134a3c582dc1e1a3f7335fd55142e5965769855a00b944e13e48302fc",
	},
	"linux-arm64": {
		filename: "toxiproxy-server-linux-arm64",
		checksum: "53e770c1c3035b5a9f1bc629fce537db1f95f62b26f4ebe6e756afd701cf077c",
	},
	"darwin-amd64": {
		filename: "toxiproxy-server-darwin-amd64",
		checksum: "9625bba4bd96117eedae49f982aba4c2f462b268dd406c9ff18186f9b1ef8afe",
	},
	"darwin-arm64": {
		filename: "toxiproxy-server-darwin-arm64",
		checksum: "aa299966b52f16a8594f1cd0d1e9049dc2e8fe2c04a90c19860e2719b2b95d15",
	},
}

// EnsureBinary reuses a verified Toxiproxy release or downloads it into the runner cache.
func EnsureBinary(
	ctx context.Context,
	runtimeDirectory string,
	reportProgress func(string),
) (string, error) {
	asset, assetError := currentReleaseAsset()
	if assetError != nil {
		return "", assetError
	}

	binaryDirectory := filepath.Join(
		runtimeDirectory,
		"tools",
		"toxiproxy",
		toxiproxyVersion,
	)
	binaryPath := filepath.Join(binaryDirectory, asset.filename)

	if binaryMatchesChecksum(binaryPath, asset.checksum) {
		chmodError := os.Chmod(binaryPath, 0o755)
		if chmodError != nil {
			return "", fmt.Errorf("make cached Toxiproxy executable: %w", chmodError)
		}

		reportProgress(fmt.Sprintf("Reusing verified %s", asset.filename))

		return binaryPath, nil
	}

	reportProgress(fmt.Sprintf("Downloading %s", asset.filename))

	binaryContents, downloadError := downloadReleaseAsset(ctx, asset)
	if downloadError != nil {
		return "", downloadError
	}

	createDirectoryError := os.MkdirAll(binaryDirectory, 0o755)
	if createDirectoryError != nil {
		return "", fmt.Errorf("create Toxiproxy cache: %w", createDirectoryError)
	}

	temporaryPath := binaryPath + ".download"
	writeError := os.WriteFile(temporaryPath, binaryContents, 0o755)
	if writeError != nil {
		return "", fmt.Errorf("write downloaded Toxiproxy binary: %w", writeError)
	}
	defer os.Remove(temporaryPath)

	renameError := os.Rename(temporaryPath, binaryPath)
	if renameError != nil {
		return "", fmt.Errorf("store downloaded Toxiproxy binary: %w", renameError)
	}

	reportProgress(fmt.Sprintf("Downloaded and verified %s", asset.filename))

	return binaryPath, nil
}

// currentReleaseAsset rejects unsupported systems before any download is attempted.
func currentReleaseAsset() (releaseAsset, error) {
	if runningInWSL() {
		return releaseAsset{}, fmt.Errorf("WSL is not supported")
	}

	platform := runtime.GOOS + "-" + runtime.GOARCH
	asset, supported := releaseAssets[platform]

	if !supported {
		return releaseAsset{}, fmt.Errorf("Toxiproxy is not available for %s", platform)
	}

	return asset, nil
}

// runningInWSL distinguishes native Linux from the unsupported Windows compatibility layer.
func runningInWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}

	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}

	kernelRelease, readError := os.ReadFile("/proc/sys/kernel/osrelease")
	if readError != nil {
		return false
	}

	return strings.Contains(strings.ToLower(string(kernelRelease)), "microsoft")
}

// binaryMatchesChecksum prevents an incomplete or modified cache entry from being executed.
func binaryMatchesChecksum(binaryPath string, expectedChecksum string) bool {
	binaryContents, readError := os.ReadFile(binaryPath)
	if readError != nil {
		return false
	}

	return checksum(binaryContents) == expectedChecksum
}

// downloadReleaseAsset verifies the response before its contents enter the local cache.
func downloadReleaseAsset(ctx context.Context, asset releaseAsset) ([]byte, error) {
	downloadURL := fmt.Sprintf(
		"%s/v%s/%s",
		toxiproxyReleaseURL,
		toxiproxyVersion,
		asset.filename,
	)

	request, requestError := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if requestError != nil {
		return nil, fmt.Errorf("create Toxiproxy download request: %w", requestError)
	}

	httpClient := &http.Client{
		Timeout: binaryDownloadTimeout,
	}

	response, requestError := httpClient.Do(request)
	if requestError != nil {
		return nil, fmt.Errorf("download Toxiproxy: %w", requestError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)

		return nil, fmt.Errorf("download Toxiproxy: HTTP %d", response.StatusCode)
	}

	binaryContents, readError := io.ReadAll(response.Body)
	if readError != nil {
		return nil, fmt.Errorf("read downloaded Toxiproxy binary: %w", readError)
	}

	if checksum(binaryContents) != asset.checksum {
		return nil, fmt.Errorf("downloaded Toxiproxy checksum does not match the release")
	}

	return binaryContents, nil
}

// checksum returns the lowercase SHA-256 representation used by release metadata.
func checksum(contents []byte) string {
	value := sha256.Sum256(contents)

	return fmt.Sprintf("%x", value)
}
