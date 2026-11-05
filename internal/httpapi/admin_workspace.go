package httpapi

import (
	"net/http"

	"github.com/yiluoiro/card-word/internal/store"
)

func (s *Server) handleAdminCustomers(w http.ResponseWriter, r *http.Request) {
	rows, view, ok := s.adminList(w, r, s.db, "customers", "")
	if !ok {
		return
	}
	s.render(w, r, "admin_customers", map[string]any{"Title": "买家管理", "Customers": rows, "List": view})
}

type workspaceTask struct {
	Label, Hint, URL string
	Count            int
}

func (s *Server) workspaceTasks(r *http.Request, low int) ([]workspaceTask, error) {
	tasks := []workspaceTask{{"库存预警", "上架商品可售库存 ≤ 3，补货后可重试发货", "/admin/products?status=active&stock=low", low}}
	add := func(label, hint, url, kind, status string) error {
		count, err := s.db.AdminCount(r.Context(), kind, store.AdminFilter{Status: status})
		if err != nil {
			return err
		}
		tasks = append(tasks, workspaceTask{label, hint, url, count})
		return nil
	}
	if err := add("待补发订单", "已收款但尚未交付，请检查库存后补发", "/admin/orders?status=paid", "orders", "paid"); err != nil {
		return nil, err
	}

	return tasks, nil
}
