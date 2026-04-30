package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ExtractionRecord tracks what was extracted from a payload image into its staging directory.
type ExtractionRecord struct {
	ImageID    string `json:"image_id"`
	StagingDir string `json:"staging_dir"`
	PayloadID  string `json:"payload_id"`
	InstanceID string `json:"instance_id"`
}

// InjectedLib describes a single library file that was copied into an executor container.
type InjectedLib struct {
	PayloadID  string `json:"payload_id"`
	LibName    string `json:"lib_name"`
	SubDir     string `json:"sub_dir"`              // "behaviors" or "interfaces"
	Shadowed   bool   `json:"shadowed,omitempty"`   // true if a newer-timestamp file from another payload won
	ShadowedBy string `json:"shadowed_by,omitempty"` // payload ID that provided the winning file
}

// InjectionRecord tracks which libraries were injected into an executor container.
type InjectionRecord struct {
	ContainerID string        `json:"container_id"`
	InstanceID  string        `json:"instance_id"`
	Libs        []InjectedLib `json:"libs"`
}

type registryData struct {
	Extractions map[string]ExtractionRecord `json:"extractions"` // imageID → record
	Injections  map[string]InjectionRecord  `json:"injections"`  // containerID → record
}

// Registry is a persistent, mutex-protected store of extraction and injection records.
// It is written atomically (temp file + rename) on every mutation.
type Registry struct {
	data registryData
	path string
	mu   sync.Mutex
}

// Load reads the registry from $libPath/registry.json, creating an empty one if absent.
func Load(libPath string) (*Registry, error) {
	path := filepath.Join(libPath, "registry.json")
	r := &Registry{
		path: path,
		data: registryData{
			Extractions: make(map[string]ExtractionRecord),
			Injections:  make(map[string]InjectionRecord),
		},
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading registry: %w", err)
	}
	if err := json.Unmarshal(raw, &r.data); err != nil {
		return nil, fmt.Errorf("parsing registry: %w", err)
	}
	if r.data.Extractions == nil {
		r.data.Extractions = make(map[string]ExtractionRecord)
	}
	if r.data.Injections == nil {
		r.data.Injections = make(map[string]InjectionRecord)
	}
	return r, nil
}

func (r *Registry) RecordExtraction(imageID, stagingDir, payloadID, instanceID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data.Extractions[imageID] = ExtractionRecord{
		ImageID:    imageID,
		StagingDir: stagingDir,
		PayloadID:  payloadID,
		InstanceID: instanceID,
	}
	return r.save()
}

// RemoveExtraction removes the record and returns the staging directory path to delete.
func (r *Registry) RemoveExtraction(imageID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.data.Extractions[imageID]
	if !ok {
		return "", nil
	}
	delete(r.data.Extractions, imageID)
	return rec.StagingDir, r.save()
}

// RemoveExtractionsForInstance removes all extraction records for an instance and
// returns their staging directories.
func (r *Registry) RemoveExtractionsForInstance(instanceID string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var dirs []string
	for imageID, rec := range r.data.Extractions {
		if rec.InstanceID == instanceID {
			dirs = append(dirs, rec.StagingDir)
			delete(r.data.Extractions, imageID)
		}
	}
	if len(dirs) == 0 {
		return nil, nil
	}
	return dirs, r.save()
}

func (r *Registry) RecordInjection(containerID, instanceID string, libs []InjectedLib) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data.Injections[containerID] = InjectionRecord{
		ContainerID: containerID,
		InstanceID:  instanceID,
		Libs:        libs,
	}
	return r.save()
}

func (r *Registry) RemoveInjection(containerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data.Injections[containerID]; !ok {
		return nil
	}
	delete(r.data.Injections, containerID)
	return r.save()
}

// RemoveInjectionsForInstance removes all injection records for an instance.
func (r *Registry) RemoveInjectionsForInstance(instanceID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	for cid, rec := range r.data.Injections {
		if rec.InstanceID == instanceID {
			delete(r.data.Injections, cid)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return r.save()
}

// GetExecutorsForPayload returns all injection records that include libs from payloadID.
func (r *Registry) GetExecutorsForPayload(payloadID string) []InjectionRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []InjectionRecord
	for _, rec := range r.data.Injections {
		for _, lib := range rec.Libs {
			if lib.PayloadID == payloadID {
				result = append(result, rec)
				break
			}
		}
	}
	return result
}

func (r *Registry) save() error {
	raw, err := json.MarshalIndent(r.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling registry: %w", err)
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return fmt.Errorf("writing registry temp: %w", err)
	}
	return os.Rename(tmp, r.path)
}
