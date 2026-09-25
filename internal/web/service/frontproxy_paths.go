package service

import (
	"fmt"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/frontproxy"

	"gorm.io/gorm"
)

// FrontProxyPathService manages the panel-wide (one shared :443, not
// per-inbound the way Fallbacks are) list of path -> XHTTP/WS-inbound
// routes, mirroring FallbackService's own shape (fallback.go).
type FrontProxyPathService struct{}

// FrontProxyPathInput is the payload shape POSTed by the settings UI.
type FrontProxyPathInput struct {
	ChildId   int    `json:"childId"`
	Path      string `json:"path"`
	SortOrder int    `json:"sortOrder"`
}

// reservedPathPrefixes are checked at SetAll time so an admin-added route can
// never shadow a secret or protocol-mandated path -- resolveTarget's own
// priority (Sub/Panel/Tproxy before any path route) already enforces this at
// request time too; this is the earlier, friendlier rejection.
var reservedPathPrefixes = []string{"api/v1"}

// GetAll returns every configured path route, in resolution order.
func (s *FrontProxyPathService) GetAll() ([]model.FrontProxyPathRoute, error) {
	var rows []model.FrontProxyPathRoute
	err := database.GetDB().Order("sort_order ASC, id ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// SetAll atomically replaces the whole path-route list.
func (s *FrontProxyPathService) SetAll(items []FrontProxyPathInput, panelBasePath, subPath string) error {
	seenPaths := make(map[string]struct{}, len(items))
	reserved := append([]string{panelBasePath, subPath}, reservedPathPrefixes...)
	for i, it := range items {
		p := normalizePath(it.Path)
		if p == "" {
			return fmt.Errorf("path route #%d: path must not be empty", i+1)
		}
		if it.ChildId <= 0 {
			return fmt.Errorf("path route #%d (%s): no inbound selected", i+1, p)
		}
		if _, dup := seenPaths[p]; dup {
			return fmt.Errorf("path route #%d: path %q is already used by another row", i+1, p)
		}
		seenPaths[p] = struct{}{}
		for _, r := range reserved {
			r = normalizePath(r)
			if r == "" {
				continue
			}
			if p == r || strings.HasPrefix(p, r+"/") || strings.HasPrefix(r, p+"/") {
				return fmt.Errorf("path route #%d: path %q collides with a reserved path (%q)", i+1, p, r)
			}
		}
	}

	db := database.GetDB()
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&model.FrontProxyPathRoute{}).Error; err != nil {
			return err
		}
		for i, it := range items {
			row := model.FrontProxyPathRoute{
				ChildId:   it.ChildId,
				Path:      normalizePath(it.Path),
				SortOrder: it.SortOrder,
			}
			if row.SortOrder == 0 {
				row.SortOrder = i
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// BuildTargets resolves every configured row's ChildId into a loopback port,
// mirroring FallbackService.BuildFallbacksJSON's own dest resolution
// (fallback.go) -- a child whose own Listen isn't loopback-equivalent is
// skipped rather than silently routed to the wrong host, since
// frontproxy.PathTarget (and the newLoopbackProxy hop it drives) only ever
// dials 127.0.0.1.
func (s *FrontProxyPathService) BuildTargets(tx *gorm.DB) ([]frontproxy.PathTarget, error) {
	if tx == nil {
		tx = database.GetDB()
	}
	var rows []model.FrontProxyPathRoute
	if err := tx.Order("sort_order ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	childIds := make([]int, 0, len(rows))
	for i := range rows {
		childIds = append(childIds, rows[i].ChildId)
	}
	var children []model.Inbound
	if err := tx.Where("id IN ?", childIds).Find(&children).Error; err != nil {
		return nil, err
	}
	byId := make(map[int]*model.Inbound, len(children))
	for i := range children {
		byId[children[i].Id] = &children[i]
	}

	out := make([]frontproxy.PathTarget, 0, len(rows))
	for _, r := range rows {
		child, ok := byId[r.ChildId]
		if !ok || !child.Enable {
			continue
		}
		listen := strings.TrimSpace(child.Listen)
		if listen != "" && listen != "0.0.0.0" && listen != "::" && listen != "::0" && listen != "127.0.0.1" {
			continue
		}
		out = append(out, frontproxy.PathTarget{Path: r.Path, Port: child.Port})
	}
	return out, nil
}

// normalizePath strips leading/trailing slashes so stored and compared paths
// share one canonical form, matching matchesPrefix's own convention
// (router.go) of trimming before comparing.
func normalizePath(p string) string {
	return strings.Trim(strings.TrimSpace(p), "/")
}
