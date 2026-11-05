package httpapi

import (
	"github.com/yiluoiro/card-word/internal/store"
	"github.com/yiluoiro/card-word/internal/update"
	"net/url"
	"strings"
	"testing"
)

func TestUpdateRequiresOwnerAndCSRF(t *testing.T) {
	s := singleStoreServer(t)
	s.updates = update.New(t.TempDir(), "v1.0.0", s.cfg.MasterKey)
	s.restart = func() { t.Fatal("unexpected restart without a staged binary") }
	viewer, vt := storeAdmin(t, s, store.RoleViewer)
	owner, ot := storeAdmin(t, s, store.RoleOwner)
	for _, path := range []string{"/admin/update/install", "/admin/update/restart", "/admin/update/repository"} {
		if r := storeRequest(s, "POST", path, nil, viewer, vt); r.Code != 403 {
			t.Fatal(path, r.Code)
		}
		if r := storeRequest(s, "POST", path, nil, owner, "invalid"); r.Code != 403 {
			t.Fatal(path, r.Code)
		}
	}
	if r := storeRequest(s, "POST", "/admin/update/repository", url.Values{"repository": {"https://github.com/test-org/store"}}, owner, ot); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := storeRequest(s, "POST", "/admin/update/restart", url.Values{"version": {"v1.0.1"}}, owner, ot); r.Code != 409 {
		t.Fatal(r.Code)
	}
	if r := storeRequest(s, "GET", "/admin", nil, viewer, ""); !strings.Contains(r.Body.String(), "data-update-open") || strings.Contains(r.Body.String(), "data-update-bind") {
		t.Fatal("incorrect viewer controls")
	}
	if r := storeRequest(s, "GET", "/admin", nil, owner, ""); !strings.Contains(r.Body.String(), "data-update-action") || !strings.Contains(r.Body.String(), "v1.0.0") {
		t.Fatal("missing owner controls")
	}
}
