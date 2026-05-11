package agent

// TenantManagement groups methods for tenant session listing.
type TenantManagement interface {
	ListTenants() ([]TenantInfo, error)
}
