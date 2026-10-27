//go:build windows

package softdist

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func EvaluateDetection(ctx context.Context, d Detection, userSID string, presenceOnly bool) (DetectionResult, error) {
	return evaluateDetection(ctx, d, presenceOnly, func(r DetectionRule, presenceOnly bool) (DetectionResult, error) {
		if r.Kind == "file_exists" || r.Kind == "file_version" {
			return detectFile(r, presenceOnly)
		}
		return detectRegistry(r, userSID, presenceOnly)
	})
}

func detectFile(r DetectionRule, presenceOnly bool) (DetectionResult, error) {
	if err := detectionPath(r.Path, false); err != nil {
		return DetectionResult{}, err
	}
	drive, err := windows.UTF16PtrFromString(strings.ReplaceAll(r.Path[:3], "/", `\`))
	if err != nil || windows.GetDriveType(drive) == windows.DRIVE_REMOTE {
		return DetectionResult{}, fmt.Errorf("network drives are not supported for installed file detection")
	}
	info, err := os.Lstat(r.Path)
	if errors.Is(err, os.ErrNotExist) {
		return DetectionResult{}, nil
	}
	if err != nil {
		return DetectionResult{}, fmt.Errorf("read file metadata: %w", err)
	}
	res := DetectionResult{Present: true}
	if presenceOnly {
		return res, nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		info, err = os.Stat(r.Path)
		if errors.Is(err, os.ErrNotExist) {
			return res, nil
		}
		if err != nil {
			return res, fmt.Errorf("read file target metadata: %w", err)
		}
	}
	if !info.Mode().IsRegular() {
		return res, nil
	}
	if r.Kind == "file_exists" {
		res.Satisfied = true
		return res, nil
	}
	version, err := nativeFileVersion(r.Path)
	if err != nil {
		return res, err
	}
	if version == "" {
		return res, nil
	}
	got, err := fileVersionParts(version)
	if err != nil {
		return res, err
	}
	want, err := fileVersionParts(r.MinimumVersion)
	if err != nil {
		return res, err
	}
	res.Satisfied = true
	for i := range got {
		if got[i] != want[i] {
			res.Satisfied = got[i] > want[i]
			break
		}
	}
	return res, nil
}

func nativeFileVersion(path string) (string, error) {
	var zero windows.Handle
	size, err := windows.GetFileVersionInfoSize(path, &zero)
	if err != nil {
		if errors.Is(err, windows.ERROR_RESOURCE_DATA_NOT_FOUND) || errors.Is(err, windows.ERROR_RESOURCE_TYPE_NOT_FOUND) || errors.Is(err, windows.ERROR_RESOURCE_NAME_NOT_FOUND) || errors.Is(err, windows.ERROR_RESOURCE_LANG_NOT_FOUND) || errors.Is(err, windows.ERROR_BAD_EXE_FORMAT) {
			return "", nil
		}
		return "", fmt.Errorf("read file version size: %w", err)
	}
	if size == 0 {
		return "", nil
	}
	if size > 16<<20 {
		return "", fmt.Errorf("file version resource exceeds limit")
	}
	buf := make([]byte, size)
	if err = windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return "", fmt.Errorf("read file version resource: %w", err)
	}
	var info *windows.VS_FIXEDFILEINFO
	var count uint32
	if err = windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&info), &count); err != nil {
		return "", fmt.Errorf("query fixed file version: %w", err)
	}
	if info == nil || count < uint32(unsafe.Sizeof(windows.VS_FIXEDFILEINFO{})) || info.Signature != 0xFEEF04BD {
		return "", fmt.Errorf("invalid fixed file version resource")
	}
	return fmt.Sprintf("%d.%d.%d.%d", info.FileVersionMS>>16, info.FileVersionMS&65535, info.FileVersionLS>>16, info.FileVersionLS&65535), nil
}

func detectRegistry(r DetectionRule, userSID string, presenceOnly bool) (DetectionResult, error) {
	root := registry.LOCAL_MACHINE
	path := r.Key
	if r.Hive == "HKCU" {
		sid, err := windows.StringToSid(userSID)
		if err != nil || userSID == "" {
			return DetectionResult{}, fmt.Errorf("HKCU detection needs the selected user's loaded profile")
		}
		root = registry.USERS
		path = sid.String() + `\` + path
		profile, profileErr := registry.OpenKey(registry.USERS, sid.String(), registry.QUERY_VALUE)
		if profileErr != nil {
			return DetectionResult{}, fmt.Errorf("selected user's registry profile is unavailable: %w", profileErr)
		}
		profile.Close()
	}
	view := uint32(registry.WOW64_64KEY)
	if r.View == "32" {
		view = registry.WOW64_32KEY
	}
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE|view)
	if errors.Is(err, registry.ErrNotExist) {
		return DetectionResult{}, nil
	}
	if err != nil {
		return DetectionResult{}, fmt.Errorf("open registry detection key: %w", err)
	}
	defer key.Close()
	if r.Kind == "registry_key" {
		return DetectionResult{Present: true, Satisfied: true}, nil
	}
	_, typ, err := key.GetValue(r.ValueName, nil)
	if errors.Is(err, registry.ErrNotExist) {
		return DetectionResult{}, nil
	}
	if err != nil {
		return DetectionResult{}, fmt.Errorf("query registry value: %w", err)
	}
	res := DetectionResult{Present: true}
	if presenceOnly {
		return res, nil
	}
	if r.ValueType == "string" {
		if typ != registry.SZ && typ != registry.EXPAND_SZ {
			return res, nil
		}
		got, _, err := key.GetStringValue(r.ValueName)
		if err != nil {
			return res, fmt.Errorf("read registry string: %w", err)
		}
		res.Satisfied = got == r.ExpectedValue
		return res, nil
	}
	if (r.ValueType == "dword" && typ != registry.DWORD) || (r.ValueType == "qword" && typ != registry.QWORD) {
		return res, nil
	}
	got, _, err := key.GetIntegerValue(r.ValueName)
	if err != nil {
		return res, fmt.Errorf("read registry integer: %w", err)
	}
	want, err := strconv.ParseUint(r.ExpectedValue, 10, 64)
	if err != nil {
		return res, fmt.Errorf("invalid expected registry integer")
	}
	res.Satisfied = got == want
	return res, nil
}
