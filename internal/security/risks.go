package security

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/assets"
	"github.com/leeska/Lodestar/internal/certs"
	"github.com/leeska/Lodestar/internal/safefetch"
)

type DomainRisk struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Domain       string `json:"domain"`
	ExpiresOn    string `json:"expires_on"`
	DaysLeft     *int   `json:"days_left"`
	DNSStatus    string `json:"dns_status"`
	DNSCheckedAt int64  `json:"dns_checked_at"`
}
type CertRisk struct {
	ID       string   `json:"id"`
	Domains  []string `json:"domains"`
	NotAfter int64    `json:"not_after"`
	DaysLeft *int     `json:"days_left"`
	Status   string   `json:"status"`
	HasError bool     `json:"has_error"`
}

func domainName(asset assets.Asset) string {
	name := strings.TrimSpace(asset.Fields["domain"])
	if name == "" {
		name = asset.Name
	}
	names, err := certs.NormalizeDomains([]string{name})
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(names[0], "*.")
	if net.ParseIP(host) != nil {
		return ""
	}
	return host
}
func (m *Module) Risks(ctx context.Context) (map[string]any, error) {
	domains := []DomainRisk{}
	certificates := []CertRisk{}
	omitted := []string{}
	if allows(ctx, "assets", action.Read) {
		items, err := m.Assets.List(ctx, assets.Filter{Kind: "domain"}, m.now())
		if err != nil {
			return nil, err
		}
		for _, asset := range items {
			risk := DomainRisk{ID: asset.ID, Name: asset.Name, Domain: domainName(asset), ExpiresOn: asset.ExpiresOn, DNSStatus: "unknown"}
			if asset.ExpiresOn != "" {
				risk.DaysLeft = asset.DaysLeft
			}
			var cached string
			err := m.DB.QueryRowContext(ctx, `SELECT domain,status,checked_at FROM security_dns WHERE asset_id=?`, asset.ID).Scan(&cached, &risk.DNSStatus, &risk.DNSCheckedAt)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if cached != risk.Domain {
				risk.DNSStatus = "unknown"
				risk.DNSCheckedAt = 0
			}
			domains = append(domains, risk)
		}
	} else {
		omitted = append(omitted, "assets")
	}
	if allows(ctx, "certs", action.Read) && m.Certs != nil {
		items, err := m.Certs.Certs(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			risk := CertRisk{ID: item.ID, Domains: item.Domains, NotAfter: item.NotAfter, Status: item.Status, HasError: item.Error != ""}
			if item.NotAfter > 0 {
				days := int(time.Unix(item.NotAfter, 0).Sub(m.now()).Hours() / 24)
				if item.NotAfter < m.now().Unix() && days == 0 {
					days = -1
				}
				risk.DaysLeft = &days
			}
			certificates = append(certificates, risk)
		}
	} else {
		omitted = append(omitted, "certs")
	}
	return map[string]any{"domains": domains, "certificates": certificates, "omitted": omitted}, nil
}
func (m *Module) CheckDNS(ctx context.Context) error {
	if !m.dns.TryLock() {
		return nil
	}
	defer m.dns.Unlock()
	config, err := m.Settings(ctx)
	if err != nil || !config.DNSChecks {
		return err
	}
	items, err := m.Assets.List(ctx, assets.Filter{Kind: "domain"}, m.now())
	if err != nil {
		return err
	}
	lookup := m.LookupIP
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	checked := 0
	for _, asset := range items {
		if checked >= 100 || ctx.Err() != nil {
			break
		}
		domain := domainName(asset)
		if domain == "" {
			continue
		}
		var last int64
		var previous string
		err := m.DB.QueryRowContext(ctx, `SELECT checked_at,domain FROM security_dns WHERE asset_id=?`, asset.ID).Scan(&last, &previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if last > m.now().Add(-24*time.Hour).Unix() && previous == domain {
			continue
		}
		query, cancel := context.WithTimeout(ctx, 5*time.Second)
		addresses, queryErr := lookup(query, domain)
		cancel()
		status := "ok"
		if queryErr != nil || len(addresses) == 0 {
			status = "error"
		} else {
			for _, address := range addresses {
				parsed, ok := netip.AddrFromSlice(address.IP)
				if !ok || address.Zone != "" || !safefetch.Public(parsed) {
					status = "private"
					break
				}
			}
		}
		if _, err := m.DB.ExecContext(ctx, `INSERT INTO security_dns(asset_id,domain,status,checked_at) VALUES(?,?,?,?) ON CONFLICT(asset_id) DO UPDATE SET domain=excluded.domain,status=excluded.status,checked_at=excluded.checked_at`, asset.ID, domain, status, m.now().Unix()); err != nil {
			return err
		}
		checked++
	}
	return nil
}
