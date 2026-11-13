package workflow

import "gorm.io/gorm"

// The source's approved catalog mapping defines the maximum field scope of a service presentation.
func serviceCatalogScope(tx *gorm.DB, directoryID string, cfg *DataServiceQuery) (SharingSnapshot, []Field, map[string]string, error) {
	var snapshot SharingSnapshot
	source, err := get(tx, "dataset", cfg.DatasetID, false)
	if err != nil {
		return snapshot, nil, nil, err
	}
	ref := Resource{Type: "governed_dataset", ID: source.ID, Version: source.Version}
	snapshot, err = resolveActiveSharing(tx, ref, false)
	if err != nil {
		return snapshot, nil, nil, err
	}
	if snapshot.DirectoryID != directoryID || (cfg.SourceSnapshot != nil && *cfg.SourceSnapshot != snapshot) {
		return snapshot, nil, nil, problem(409, "SERVICE_SOURCE_CATALOG_CHANGED", "来源成果的目录或挂载已变化，请按当前来源重新登记服务")
	}
	revision, err := get(tx, "directory_revision", snapshot.DirectoryRevisionID, false)
	if err != nil {
		return snapshot, nil, nil, err
	}
	directory, err := decode[DirectoryRevisionData](revision.Body)
	if err != nil {
		return snapshot, nil, nil, err
	}
	mountRevision, err := get(tx, "mount_revision", snapshot.MountRevisionID, false)
	if err != nil {
		return snapshot, nil, nil, err
	}
	mount, err := decode[MountRevisionData](mountRevision.Body)
	if err != nil {
		return snapshot, nil, nil, err
	}
	shared := []Field{}
	mapping := map[string]string{}
	for _, item := range directory.Definition.Items {
		name := mount.Mapping[item.Name]
		for _, field := range cfg.SourceFields {
			if field.Name == name {
				field.Sensitive = field.Sensitive || item.Sensitive
				shared = append(shared, field)
				mapping[item.Name] = name
				break
			}
		}
	}
	return snapshot, shared, mapping, nil
}

func serviceMountVariant(tx *gorm.DB, directoryID string, ref Resource) (bool, error) {
	if ref.Type != "api_resource" {
		return false, nil
	}
	row, err := get(tx, "api_resource", ref.ID, false)
	if err != nil {
		return false, err
	}
	value, err := decode[ManagedResource](row.Body)
	if err != nil {
		return false, err
	}
	cfg, err := decode[APIConfig](value.Config)
	if err != nil {
		return false, err
	}
	if cfg.DataService == nil || cfg.DataService.SourceSnapshot == nil {
		return false, nil
	}
	if row.ParentID != directoryID || cfg.DataService.SourceSnapshot.DirectoryID != directoryID {
		return false, problem(409, "SERVICE_CATALOG_MISMATCH", "查询服务只能挂载在其来源业务目录内")
	}
	_, fields, mapping, err := serviceCatalogScope(tx, directoryID, cfg.DataService)
	if err != nil {
		return false, err
	}
	allowed := map[string]bool{}
	for _, field := range fields {
		allowed[field.Name] = true
	}
	for _, field := range value.Fields {
		if !allowed[field.Name] {
			return false, denied
		}
	}
	if len(mapping) == 0 {
		return false, invalid
	}
	return true, nil
}
