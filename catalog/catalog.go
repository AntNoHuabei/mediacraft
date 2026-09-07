package catalog

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

type Localized struct {
	En string `json:"en"`
	Zh string `json:"zh"`
}
type Download struct {
	URL      string `json:"url"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	SHA256   string `json:"sha_256"`
}
type XPU struct {
	Vendor           string                `json:"vendor"`
	Type             string                `json:"type"`
	InferenceEngines []string              `json:"inference_engines"`
	Downloads        map[string][]Download `json:"downloads"`
	RuntimeVersion   string                `json:"runtime_version"`
	Parameters       map[string]any        `json:"parameters"`
}
type Architecture struct {
	Arch string `json:"arch"`
	OS   string `json:"os"`
	XPUs []XPU  `json:"xpus"`
}
type Manifest struct {
	Name          string         `json:"name"`
	Version       string         `json:"version"`
	Type          string         `json:"type"`
	DisplayName   Localized      `json:"display_name"`
	Description   Localized      `json:"description"`
	Capabilities  []string       `json:"capabilities"`
	Tags          []string       `json:"tags"`
	Architectures []Architecture `json:"architectures"`
	Parameters    map[string]any `json:"parameters"`
}
type RuntimeVersion struct {
	Version   string            `json:"version"`
	Downloads map[string]string `json:"downloads"`
	FileSize  int64             `json:"file_size"`
	SHA256    string            `json:"sha_256"`
}
type RuntimeXPU struct {
	Vendor   string           `json:"vendor"`
	Type     string           `json:"type"`
	Versions []RuntimeVersion `json:"versions"`
}
type RuntimeArchitecture struct {
	Arch string       `json:"arch"`
	OS   string       `json:"os"`
	XPUs []RuntimeXPU `json:"xpus"`
}
type Runtime struct {
	Name          string                `json:"name"`
	DisplayName   Localized             `json:"display_name"`
	Description   Localized             `json:"description"`
	Architectures []RuntimeArchitecture `json:"architectures"`
}

func (r Runtime) Select(goos, arch, vendor, required string) (RuntimeXPU, RuntimeVersion, string, bool) {
	for _, a := range r.Architectures {
		if !strings.EqualFold(a.OS, goos) || !strings.EqualFold(a.Arch, arch) {
			continue
		}
		for pass := 0; pass < 2; pass++ {
			for _, x := range a.XPUs {
				if (pass == 0 && !strings.EqualFold(x.Vendor, vendor)) || (pass == 1 && !strings.EqualFold(x.Vendor, "all")) {
					continue
				}
				for i := len(x.Versions) - 1; i >= 0; i-- {
					v := x.Versions[i]
					if required != "" && v.Version != required {
						continue
					}
					if url := v.Downloads["model_scope"]; url != "" {
						return x, v, url, true
					}
					for _, url := range v.Downloads {
						if url != "" {
							return x, v, url, true
						}
					}
				}
			}
		}
	}
	return RuntimeXPU{}, RuntimeVersion{}, "", false
}
func (r Runtime) WindowsDownload() (RuntimeVersion, string, bool) {
	_, v, url, ok := r.Select("windows", "amd64", "all", "")
	return v, url, ok
}
func (m Manifest) MatchPlatform(goos, arch, vendor string) (*Architecture, *XPU) {
	for i := range m.Architectures {
		a := &m.Architectures[i]
		if !strings.EqualFold(a.OS, goos) || !strings.EqualFold(a.Arch, arch) {
			continue
		}
		for j := range a.XPUs {
			if strings.EqualFold(a.XPUs[j].Vendor, vendor) {
				return a, &a.XPUs[j]
			}
		}
		for j := range a.XPUs {
			if strings.EqualFold(a.XPUs[j].Vendor, "all") {
				return a, &a.XPUs[j]
			}
		}
	}
	return nil, nil
}
func (m Manifest) ResolveInferenceEngine(goos, arch, vendor string) ([]string, *XPU, error) {
	_, xpu := m.MatchPlatform(goos, arch, vendor)
	if xpu == nil || len(xpu.InferenceEngines) == 0 {
		return nil, nil, fmt.Errorf("runtime not specified for model: %s", m.Name)
	}
	return append([]string(nil), xpu.InferenceEngines...), xpu, nil
}
func (m Manifest) MergedParameters(xpu *XPU) map[string]any {
	out := map[string]any{}
	for k, v := range m.Parameters {
		out[k] = v
	}
	if xpu != nil {
		for k, v := range xpu.Parameters {
			out[k] = v
		}
	}
	return out
}

type Catalog struct {
	Models   []Manifest
	Runtimes []Runtime
}

func Load(fsys fs.FS) (Catalog, error) {
	var result Catalog
	entries, err := fs.Glob(fsys, "manifest/models/*.json")
	if err != nil {
		return result, err
	}
	for _, path := range entries {
		var m Manifest
		if err := readJSON(fsys, path, &m); err != nil {
			return result, fmt.Errorf("read model %s: %w", path, err)
		}
		result.Models = append(result.Models, m)
	}
	entries, err = fs.Glob(fsys, "manifest/runtimes/*.json")
	if err != nil {
		return result, err
	}
	for _, path := range entries {
		var r Runtime
		if err := readJSON(fsys, path, &r); err != nil {
			return result, fmt.Errorf("read runtime %s: %w", path, err)
		}
		result.Runtimes = append(result.Runtimes, r)
	}
	return result, nil
}
func readJSON(fsys fs.FS, path string, out any) error {
	data, err := fs.ReadFile(fsys, filepath.ToSlash(path))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
func (m Manifest) SupportsCurrentPlatform() bool {
	_, x := m.MatchPlatform("windows", "amd64", "all")
	return x != nil
}
func (m Manifest) RuntimeNames() []string {
	seen := map[string]bool{}
	var names []string
	for _, a := range m.Architectures {
		for _, x := range a.XPUs {
			for _, n := range x.InferenceEngines {
				if !seen[n] {
					names = append(names, n)
					seen[n] = true
				}
			}
		}
	}
	return names
}
func (m Manifest) Display() string {
	if strings.TrimSpace(m.DisplayName.Zh) != "" {
		return m.DisplayName.Zh
	}
	if m.DisplayName.En != "" {
		return m.DisplayName.En
	}
	return m.Name
}
