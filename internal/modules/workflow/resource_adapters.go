package workflow

import "gorm.io/gorm"

func managedResource(tx *gorm.DB, ref Resource, lock bool) (Record, ManagedResource, error) {
	r, e := get(tx, ref.Type, ref.ID, lock)
	if e != nil {
		return r, ManagedResource{}, e
	}
	if r.Version != ref.Version {
		return r, ManagedResource{}, problem(409, "CONTRACT_VERSION_CHANGED", "资源契约版本已变化")
	}
	if r.State != "ready" {
		return r, ManagedResource{}, problem(409, "RESOURCE_NOT_READY", "资源尚未准备完成")
	}
	v, e := decode[ManagedResource](r.Body)
	if e == nil && v.DataLayer != "raw_object" && v.DataLayer != "governed_dataset" {
		e = invalid
	}
	return r, v, e
}
func resourceLayer(tx *gorm.DB, ref Resource) (string, error) {
	if !extendedResource(ref.Type) {
		if ref.Type != "raw_object" && ref.Type != "governed_dataset" {
			return "", invalid
		}
		return ref.Type, nil
	}
	_, v, e := managedResource(tx, ref, false)
	return v.DataLayer, e
}
