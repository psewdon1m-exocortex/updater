package engine

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"updater/internal/config"
)

type deploymentFile struct {
	Name    string `json:"name"`
	Existed bool   `json:"existed"`
	Data    []byte `json:"data,omitempty"`
}

// Only versioned deployment files are applied. Environment, trust, state and
// bundled installers are never executed or overwritten by an application update.
func deploymentNames(service string) map[string]bool {
	names := map[string]bool{"compose.production.yaml": true}
	if service == "saturn" {
		names["infra/production/Caddyfile"] = true
	}
	return names
}

func readDeployment(bundle, service string) (map[string][]byte, error) {
	if bundle == "" {
		return nil, errors.New("verified deployment bundle is missing")
	}
	wanted, result, seen := deploymentNames(service), map[string][]byte{}, map[string]bool{}
	var total int64
	accept := func(name string, size int64, regular bool, r io.Reader) error {
		name = strings.TrimPrefix(name, "./")
		if name == "" || name == "." {
			return nil
		}
		if strings.ContainsAny(name, "\\\x00:") || strings.HasPrefix(name, "/") || path.Clean(name) != strings.TrimSuffix(name, "/") || size < 0 {
			return errors.New("invalid deployment bundle member")
		}
		total += size
		if total > 256*1024*1024 || len(seen) >= 10000 || seen[name] {
			return errors.New("deployment bundle limits or duplicate member")
		}
		seen[name] = true
		if !regular {
			if strings.HasSuffix(name, "/") {
				return nil
			}
			return errors.New("deployment bundle links and special files are forbidden")
		}
		if !wanted[name] {
			return nil
		}
		if size > 1024*1024 {
			return errors.New("deployment configuration exceeds limit")
		}
		data, err := io.ReadAll(io.LimitReader(r, 1024*1024+1))
		if err != nil {
			return err
		}
		if int64(len(data)) != size {
			return errors.New("deployment member length mismatch")
		}
		result[name] = data
		return nil
	}
	if service == "saturn" {
		archive, err := zip.OpenReader(bundle)
		if err != nil {
			return nil, err
		}
		defer archive.Close()
		for _, entry := range archive.File {
			if entry.UncompressedSize64 > 256*1024*1024 {
				return nil, errors.New("deployment member exceeds limit")
			}
			stream, err := entry.Open()
			if err != nil {
				return nil, err
			}
			err = accept(entry.Name, int64(entry.UncompressedSize64), entry.Mode().IsRegular(), stream)
			stream.Close()
			if err != nil {
				return nil, err
			}
		}
	} else {
		file, err := os.Open(bundle)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		compressed, err := gzip.NewReader(file)
		if err != nil {
			return nil, err
		}
		defer compressed.Close()
		reader := tar.NewReader(compressed)
		for {
			h, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, err
			}
			if err = accept(h.Name, h.Size, h.Typeflag == tar.TypeReg, reader); err != nil {
				return nil, err
			}
		}
	}
	for name := range wanted {
		if len(result[name]) == 0 {
			return nil, errors.New("required deployment configuration is missing: " + name)
		}
	}
	return result, nil
}

func deploymentTarget(head config.HeadConfig, name string) (string, error) {
	if !deploymentNames(head.Service)[name] {
		return "", errors.New("invalid deployment target")
	}
	target := filepath.Join(head.ProjectDir, filepath.FromSlash(name))
	if name == "compose.production.yaml" {
		target = filepath.Join(head.ProjectDir, head.ComposeFile)
	}
	rel, err := filepath.Rel(head.ProjectDir, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("deployment target escapes project")
	}
	current := head.ProjectDir
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("deployment target is a symbolic link")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return target, nil
}

func atomicDeploymentWrite(filename string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0750); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(filename), ".updater-deployment-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), filename); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(filename))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func snapshotDeployment(head config.HeadConfig, files map[string][]byte, snapshot string) error {
	previous := []deploymentFile{}
	for name := range files {
		target, err := deploymentTarget(head, name)
		if err != nil {
			return err
		}
		info, err := os.Stat(target)
		if errors.Is(err, os.ErrNotExist) {
			previous = append(previous, deploymentFile{Name: name})
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
			return errors.New("existing deployment file is invalid")
		}
		data, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		previous = append(previous, deploymentFile{Name: name, Existed: true, Data: data})
	}
	body, err := json.Marshal(previous)
	if err != nil {
		return err
	}
	return atomicDeploymentWrite(snapshot, body)
}

func applyDeployment(head config.HeadConfig, files map[string][]byte) error {
	for name, data := range files {
		target, err := deploymentTarget(head, name)
		if err != nil {
			return err
		}
		if err = atomicDeploymentWrite(target, data); err != nil {
			return err
		}
	}
	return nil
}

func restoreDeployment(head config.HeadConfig, snapshot string) error {
	if snapshot == "" {
		return nil
	} // Older jobs did not replace deployment files.
	info, err := os.Stat(snapshot)
	if err != nil {
		return err
	}
	if info.Size() > 4*1024*1024 {
		return errors.New("deployment snapshot exceeds limit")
	}
	body, err := os.ReadFile(snapshot)
	if err != nil {
		return err
	}
	var files []deploymentFile
	if err = json.Unmarshal(body, &files); err != nil {
		return err
	}
	if len(files) != len(deploymentNames(head.Service)) {
		return errors.New("deployment snapshot is incomplete")
	}
	seen := map[string]bool{}
	for _, file := range files {
		target, err := deploymentTarget(head, file.Name)
		if err != nil {
			return err
		}
		if seen[file.Name] {
			return errors.New("duplicate deployment snapshot member")
		}
		seen[file.Name] = true
		if file.Existed {
			err = atomicDeploymentWrite(target, file.Data)
		} else {
			err = os.Remove(target)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}
