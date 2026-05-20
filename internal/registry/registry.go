package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// tracks what was extracted from a payload image into its staging directory; InstanceIDs is a reference-counted set: every instance that has referenced (extracted or injected from) this staging directory holds a slot
type ExtractionRecord struct {
	ImageID      string   `json:"image_id"`
	StagingDir   string   `json:"staging_dir"`
	PayloadID    string   `json:"payload_id"`
	InstanceIDs  []string `json:"instance_ids"`
	BtcppVersion string   `json:"btcpp_version,omitempty"`
	RosDistro    string   `json:"ros_distro,omitempty"`
	// Deprecated: present only in old registry files; migrated to InstanceIDs on load.
	InstanceID string `json:"instance_id,omitempty"`
}

// describes a single library file that was copied into an executor container
type InjectedLib struct {
	PayloadID  string `json:"payload_id"`
	LibName    string `json:"lib_name"`
	SubDir     string `json:"sub_dir"`               // "behaviors" or "interfaces"
	Shadowed   bool   `json:"shadowed,omitempty"`    // true if a newer-timestamp file from another payload won
	ShadowedBy string `json:"shadowed_by,omitempty"` // payload ID that provided the winning file
}

// tracks which libraries were injected into an executor container
type InjectionRecord struct {
	ContainerID string        `json:"container_id"`
	InstanceID  string        `json:"instance_id"`
	Libs        []InjectedLib `json:"libs"`
}

type registryData struct {
	Extractions map[string]ExtractionRecord `json:"extractions"` // imageID -> record
	Injections  map[string]InjectionRecord  `json:"injections"`  // containerID -> record
}

// persistent, mutex-protected store of extraction and injection records; it is written atomically (temp file + rename) on every mutation
type Registry struct {
	data registryData
	path string
	mu   sync.Mutex
}

// reads the registry from $libPath/registry.json, creating an empty one if absent; old registry files using the deprecated single-string instance_id field are migrated automatically
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
	// Migrate old single instance_id → instance_ids.
	for imageID, rec := range r.data.Extractions {
		if len(rec.InstanceIDs) == 0 && rec.InstanceID != "" {
			rec.InstanceIDs = []string{rec.InstanceID}
			rec.InstanceID = ""
			r.data.Extractions[imageID] = rec
		}
	}
	return r, nil
}

// adds instanceID to the reference set for imageID; if the record does not yet exist, it is created with stagingDir/payloadID/versions; if it exists, those fields are left unchanged (the first extractor's values are canonical)
func (r *Registry) RecordExtraction(imageID, stagingDir, payloadID, instanceID, btcppVersion, rosDistro string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, exists := r.data.Extractions[imageID]
	if exists {
		if containsStr(rec.InstanceIDs, instanceID) {
			return nil // already recorded, no write needed
		}
		rec.InstanceIDs = append(rec.InstanceIDs, instanceID)
		r.data.Extractions[imageID] = rec
		return r.save()
	}
	r.data.Extractions[imageID] = ExtractionRecord{
		ImageID:      imageID,
		StagingDir:   stagingDir,
		PayloadID:    payloadID,
		InstanceIDs:  []string{instanceID},
		BtcppVersion: btcppVersion,
		RosDistro:    rosDistro,
	}
	return r.save()
}

// returns a snapshot of all currently recorded extraction records; read-only — injection does not hold a producer reference
func (r *Registry) AllExtractions() map[string]ExtractionRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]ExtractionRecord, len(r.data.Extractions))
	for imageID, rec := range r.data.Extractions {
		result[imageID] = rec
	}
	return result
}

// removes instanceID from the reference set for imageID; the staging directory path is returned (and should be deleted by the caller) only when the reference set becomes empty; an empty return value means other instances still hold the directory
func (r *Registry) RemoveExtraction(imageID, instanceID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.data.Extractions[imageID]
	if !ok {
		return "", nil
	}
	newIDs := removeStr(rec.InstanceIDs, instanceID)
	if len(newIDs) == len(rec.InstanceIDs) {
		return "", nil // instanceID was not in the set
	}
	if len(newIDs) == 0 {
		delete(r.data.Extractions, imageID)
		return rec.StagingDir, r.save()
	}
	rec.InstanceIDs = newIDs
	r.data.Extractions[imageID] = rec
	return "", r.save()
}

// removes instanceID from the reference set of every extraction record; it returns the staging directory paths whose reference counts have dropped to zero (those are safe to delete)
func (r *Registry) RemoveExtractionsForInstance(instanceID string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var dirsToDelete []string
	changed := false
	for imageID, rec := range r.data.Extractions {
		newIDs := removeStr(rec.InstanceIDs, instanceID)
		if len(newIDs) == len(rec.InstanceIDs) {
			continue // instanceID was not in this record
		}
		changed = true
		if len(newIDs) == 0 {
			dirsToDelete = append(dirsToDelete, rec.StagingDir)
			delete(r.data.Extractions, imageID)
		} else {
			rec.InstanceIDs = newIDs
			r.data.Extractions[imageID] = rec
		}
	}
	if !changed {
		return nil, nil
	}
	return dirsToDelete, r.save()
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

// removes all injection records for an instance
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

// returns all injection records that include libs from payloadID
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

// removes registry.json when both maps are empty; call after any cleanup operation as a best-effort housekeeping step
func (r *Registry) CleanupIfEmpty() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.data.Extractions) == 0 && len(r.data.Injections) == 0 {
		if err := os.Remove(r.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
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

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func removeStr(slice []string, s string) []string {
	result := make([]string, 0, len(slice))
	for _, v := range slice {
		if v != s {
			result = append(result, v)
		}
	}
	return result
}
