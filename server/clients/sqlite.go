package clients

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/cloudradar-monitoring/rport/db/migration/clients"
	"github.com/cloudradar-monitoring/rport/db/sqlite"
	"github.com/cloudradar-monitoring/rport/share/models"
)

type ClientProvider interface {
	GetAll(ctx context.Context) ([]*Client, error)
	Save(ctx context.Context, client *Client) error
	DeleteObsolete(ctx context.Context) error
	Delete(ctx context.Context, id string) error
	Close() error
}

type SqliteProvider struct {
	db              *sqlx.DB
	keepLostClients time.Duration
}

func NewSqliteProvider(dbPath string, keepLostClients time.Duration) (*SqliteProvider, error) {
	db, err := sqlite.New(dbPath, clients.AssetNames(), clients.Asset)
	if err != nil {
		return nil, fmt.Errorf("failed to create clients DB instance: %v", err)
	}
	return &SqliteProvider{db: db, keepLostClients: keepLostClients}, nil
}

func (p *SqliteProvider) GetAll(ctx context.Context) ([]*Client, error) {
	var res []*clientSqlite
	err := p.db.SelectContext(
		ctx,
		&res,
		"SELECT * FROM clients WHERE disconnected_at IS NULL OR DATETIME(disconnected_at) >= DATETIME(?)",
		p.keepLostClientsStart(),
	)
	if err != nil {
		return nil, err
	}
	return convertClientList(res), nil
}

func (p *SqliteProvider) get(ctx context.Context, id string) (*Client, error) {
	res := &clientSqlite{}
	err := p.db.GetContext(ctx, res, "SELECT * FROM clients WHERE id = ?", id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return res.convert(), nil
}

func (p *SqliteProvider) Save(ctx context.Context, client *Client) error {
	_, err := p.db.NamedExecContext(
		ctx,
		"INSERT OR REPLACE INTO clients (id, client_auth_id, disconnected_at, details) VALUES (:id, :client_auth_id, :disconnected_at, :details)",
		convertToSqlite(client),
	)
	return err
}

func (p *SqliteProvider) DeleteObsolete(ctx context.Context) error {
	_, err := p.db.ExecContext(
		ctx,
		"DELETE FROM clients WHERE disconnected_at IS NOT NULL AND DATETIME(disconnected_at) < DATETIME(?)",
		p.keepLostClientsStart(),
	)
	return err
}

func (p *SqliteProvider) Delete(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, "DELETE FROM clients WHERE id = ?", id)
	return err
}

func (p *SqliteProvider) keepLostClientsStart() time.Time {
	return now().Add(-p.keepLostClients)
}

func convertToSqlite(v *Client) *clientSqlite {
	if v == nil {
		return nil
	}
	res := &clientSqlite{
		ID:           v.ID,
		ClientAuthID: v.ClientAuthID,
		Details: &clientDetails{
			Name:                   v.Name,
			OS:                     v.OS,
			OSArch:                 v.OSArch,
			OSFamily:               v.OSFamily,
			OSKernel:               v.OSKernel,
			Hostname:               v.Hostname,
			Version:                v.Version,
			Address:                v.Address,
			OSFullName:             v.OSFullName,
			OSVersion:              v.OSVersion,
			OSVirtualizationSystem: v.OSVirtualizationSystem,
			OSVirtualizationRole:   v.OSVirtualizationRole,
			CPUFamily:              v.CPUFamily,
			CPUModel:               v.CPUModel,
			CPUModelName:           v.CPUModelName,
			CPUVendor:              v.CPUVendor,
			NumCPUs:                v.NumCPUs,
			MemoryTotal:            v.MemoryTotal,
			Timezone:               v.Timezone,
			IPv4:                   v.IPv4,
			IPv6:                   v.IPv6,
			Tags:                   v.Tags,
			Tunnels:                v.Tunnels,
			AllowedUserGroups:      v.AllowedUserGroups,
			UpdatesStatus:          v.UpdatesStatus,
		},
	}
	if v.DisconnectedAt != nil {
		res.DisconnectedAt = sql.NullTime{Time: *v.DisconnectedAt, Valid: true}
	}
	return res
}

type clientSqlite struct {
	ID             string         `db:"id"`
	ClientAuthID   string         `db:"client_auth_id"`
	DisconnectedAt sql.NullTime   `db:"disconnected_at"` // DisconnectedAt is a time when a client was disconnected. If nil - it's connected.
	Details        *clientDetails `db:"details"`
}

type clientDetails struct {
	NumCPUs                int                   `json:"num_cpus"`
	MemoryTotal            uint64                `json:"mem_total"`
	Name                   string                `json:"name"`
	OS                     string                `json:"os"`
	OSArch                 string                `json:"os_arch"`
	OSFamily               string                `json:"os_family"`
	OSKernel               string                `json:"os_kernel"`
	OSFullName             string                `json:"os_full_name"`
	OSVersion              string                `json:"os_version"`
	OSVirtualizationSystem string                `json:"os_virtualization_system"`
	OSVirtualizationRole   string                `json:"os_virtualization_role"`
	CPUFamily              string                `json:"cpu_family"`
	CPUModel               string                `json:"cpu_model"`
	CPUModelName           string                `json:"cpu_model_name"`
	CPUVendor              string                `json:"cpu_vendor"`
	Timezone               string                `json:"timezone"`
	Hostname               string                `json:"hostname"`
	Version                string                `json:"version"`
	Address                string                `json:"address"`
	IPv4                   []string              `json:"ipv4"`
	IPv6                   []string              `json:"ipv6"`
	Tags                   []string              `json:"tags"`
	Tunnels                []*Tunnel             `json:"tunnels"`
	AllowedUserGroups      []string              `json:"allowed_user_groups"`
	UpdatesStatus          *models.UpdatesStatus `json:"updates_status"`
}

func (d *clientDetails) Scan(value interface{}) error {
	if d == nil {
		return errors.New("'details' cannot be nil")
	}
	valueStr, ok := value.(string)
	if !ok {
		return fmt.Errorf("expected to have string, got %T", value)
	}
	err := json.Unmarshal([]byte(valueStr), d)
	if err != nil {
		return fmt.Errorf("failed to decode 'details' field: %v", err)
	}
	return nil
}

func (d *clientDetails) Value() (driver.Value, error) {
	if d == nil {
		return nil, errors.New("'details' cannot be nil")
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("failed to encode 'details' field: %v", err)
	}
	return string(b), nil
}

func (s *clientSqlite) convert() *Client {
	d := s.Details
	res := &Client{
		ID:                     s.ID,
		ClientAuthID:           s.ClientAuthID,
		Name:                   d.Name,
		OS:                     d.OS,
		OSArch:                 d.OSArch,
		OSFamily:               d.OSFamily,
		OSKernel:               d.OSKernel,
		Hostname:               d.Hostname,
		IPv4:                   d.IPv4,
		IPv6:                   d.IPv6,
		Tags:                   d.Tags,
		Version:                d.Version,
		Address:                d.Address,
		Tunnels:                d.Tunnels,
		OSFullName:             d.OSFullName,
		OSVersion:              d.OSVersion,
		OSVirtualizationSystem: d.OSVirtualizationSystem,
		OSVirtualizationRole:   d.OSVirtualizationRole,
		CPUFamily:              d.CPUFamily,
		CPUModel:               d.CPUModel,
		CPUModelName:           d.CPUModelName,
		CPUVendor:              d.CPUVendor,
		NumCPUs:                d.NumCPUs,
		MemoryTotal:            d.MemoryTotal,
		Timezone:               d.Timezone,
		AllowedUserGroups:      d.AllowedUserGroups,
		UpdatesStatus:          d.UpdatesStatus,
	}
	if s.DisconnectedAt.Valid {
		res.DisconnectedAt = &s.DisconnectedAt.Time
	}
	return res
}

func (p *SqliteProvider) Close() error {
	return p.db.Close()
}

func convertClientList(list []*clientSqlite) []*Client {
	res := make([]*Client, 0, len(list))
	for _, cur := range list {
		res = append(res, cur.convert())
	}
	return res
}
