package semanticrun

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// stageToolchain gives SDK discovery exactly one choice without changing the
// committed global.json. Trusted installed SDK/runtime files are referenced,
// not copied into the dependency bundle. The host executable is privately copied
// so its installation root is the view (a symlink would resolve to the old root).
func stageToolchain(ctx context.Context, dotnet, sdk, destination string) (string, error) {
	host, err := filepath.EvalSymlinks(dotnet)
	if err != nil {
		return "", err
	}
	sdk, err = filepath.EvalSymlinks(sdk)
	if err != nil {
		return "", err
	}
	if filepath.Base(filepath.Dir(sdk)) != "sdk" {
		return "", fmt.Errorf("semantic capture: SDK must be an installed sdk/version directory")
	}
	if info, err := os.Stat(filepath.Join(sdk, "MSBuild.dll")); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("semantic capture: selected SDK lacks MSBuild.dll")
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return "", err
	}
	if err := os.Mkdir(filepath.Join(destination, "sdk"), 0700); err != nil {
		return "", err
	}
	if err := os.Symlink(sdk, filepath.Join(destination, "sdk", filepath.Base(sdk))); err != nil {
		return "", err
	}
	installation := filepath.Dir(host)
	sdkInstallation := filepath.Dir(filepath.Dir(sdk))
	for _, name := range []string{"host", "shared", "packs", "sdk-manifests"} {
		base := installation
		if name == "packs" || name == "sdk-manifests" {
			base = sdkInstallation
		}
		source := filepath.Join(base, name)
		info, err := os.Stat(source)
		if os.IsNotExist(err) && (name == "packs" || name == "sdk-manifests") {
			continue
		}
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("semantic capture: installed toolchain directory %s unavailable", source)
		}
		if err := os.Symlink(source, filepath.Join(destination, name)); err != nil {
			return "", err
		}
	}
	in, err := os.Open(host)
	if err != nil {
		return "", err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return "", fmt.Errorf("semantic capture: dotnet host exceeds 32 MiB or is not regular")
	}
	output := filepath.Join(destination, filepath.Base(host))
	out, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return "", err
	}
	n, copyErr := io.Copy(out, io.LimitReader(dependencyReader{ctx, in}, info.Size()+1))
	closeErr := out.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n != info.Size() {
		return "", fmt.Errorf("semantic capture: dotnet host changed during staging")
	}
	return output, ctx.Err()
}
