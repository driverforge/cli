package driverforgeapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
)

// stubLogin saves a non-expired credential with no refresh token, so
// auth.Current returns it without any network call.
func stubLogin(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DRIVERFORGE_CREDENTIALS_BACKEND", "file")
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("DRIVERFORGE_AUTH_URL", "https://auth.example.test")
	if err := auth.Save(auth.Credentials{
		AccessToken: "test-token",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("stub login: %v", err)
	}
}

func TestCentrifugoToken(t *testing.T) {
	stubLogin(t)

	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"centrifugoToken":"header.payload.sig"}}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	tok, err := New().CentrifugoToken(context.Background())
	if err != nil {
		t.Fatalf("CentrifugoToken: %v", err)
	}
	if tok != "header.payload.sig" {
		t.Errorf("token = %q, want header.payload.sig", tok)
	}
	if !strings.Contains(gotBody, "centrifugoToken") {
		t.Errorf("request body missing query: %s", gotBody)
	}
}

func TestListOrganizations(t *testing.T) {
	stubLogin(t)

	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"myOrganizations":[
			{"id":"u1","name":"Acme Corp","slug":"acme-corp","myRole":"OWNER"},
			{"id":"u2","name":"Eng Test","slug":"eng-test","myRole":"MEMBER"}
		]}}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	orgs, err := New().ListOrganizations(context.Background())
	if err != nil {
		t.Fatalf("ListOrganizations: %v", err)
	}
	if len(orgs) != 2 || orgs[0].Slug != "acme-corp" || orgs[0].Role != "OWNER" || orgs[1].ID != "u2" {
		t.Errorf("unexpected orgs: %+v", orgs)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer test-token", gotAuth)
	}
	if !strings.Contains(gotBody, "myOrganizations") {
		t.Errorf("request body missing query: %s", gotBody)
	}
	// Body must be valid JSON with a query field.
	var req map[string]any
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil || req["query"] == nil {
		t.Errorf("request body is not a GraphQL envelope: %s", gotBody)
	}
}

func TestListDevices(t *testing.T) {
	stubLogin(t)

	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"deployableDevices":[
			{"id":"d1","friendlyName":"Living Room","hostname":"ea5-primary","ip":"192.168.1.10","deviceId":"00:0D:6F:AB:12:34","platform":"CONTROL4","status":"ACTIVE"},
			{"id":"d2","friendlyName":"","hostname":"ea5-secondary","ip":"192.168.1.11","deviceId":"00:0D:6F:CD:56:78","platform":"CONTROL4","status":"REVOKED"}
		]}}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	devs, err := New().ListDevices(context.Background(), "org-uuid-123")
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devs) != 2 || devs[0].ID != "d1" || devs[1].Status != "REVOKED" {
		t.Errorf("unexpected devices: %+v", devs)
	}
	if devs[0].DisplayName() != "Living Room" {
		t.Errorf("DisplayName[0] = %q, want friendlyName", devs[0].DisplayName())
	}
	if devs[1].DisplayName() != "ea5-secondary" {
		t.Errorf("DisplayName[1] = %q, want hostname fallback", devs[1].DisplayName())
	}
	if (Device{DeviceID: "MAC"}).DisplayName() != "MAC" {
		t.Error("DisplayName should fall back to deviceId")
	}
	if !strings.Contains(gotBody, "org-uuid-123") {
		t.Errorf("organizationId variable not sent: %s", gotBody)
	}
	if !strings.Contains(gotBody, "deployableDevices") {
		t.Errorf("expected deployableDevices query, got: %s", gotBody)
	}
}

func TestDeviceFingerprint(t *testing.T) {
	stubLogin(t)
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"device":{"deviceId":"00:0D:6F:AB:12:34","certFingerprint":"sha256:abc123"}}}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	fp, err := New().DeviceFingerprint(context.Background(), "00:0D:6F:AB:12:34")
	if err != nil {
		t.Fatalf("DeviceFingerprint: %v", err)
	}
	if fp != "sha256:abc123" {
		t.Errorf("fingerprint = %q, want sha256:abc123", fp)
	}
	if !strings.Contains(gotBody, "00:0D:6F:AB:12:34") || !strings.Contains(gotBody, "certFingerprint") {
		t.Errorf("request didn't carry deviceId/field: %s", gotBody)
	}
}

func TestDeviceFingerprintNull(t *testing.T) {
	stubLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"device":{"deviceId":"MAC","certFingerprint":null}}}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	fp, err := New().DeviceFingerprint(context.Background(), "MAC")
	if err != nil || fp != "" {
		t.Errorf("null certFingerprint should yield (\"\", nil), got (%q, %v)", fp, err)
	}
}

func TestGraphQLErrorSurfaced(t *testing.T) {
	stubLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"errors":[{"message":"forbidden"}]}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	_, err := New().ListOrganizations(context.Background())
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("expected GraphQL error surfaced, got %v", err)
	}
}

func TestServerErrorIsFriendly(t *testing.T) {
	stubLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	_, err := New().ListProjects(context.Background(), "org-1")
	if !clierr.IsUser(err) {
		t.Fatalf("a 502 should be a friendly UserError (not reported), got %v", err)
	}
}

func TestUnauthorizedIsFriendly(t *testing.T) {
	stubLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	_, err := New().ListOrganizations(context.Background())
	if !clierr.IsUser(err) {
		t.Errorf("a 401 should be a friendly UserError, got %v", err)
	}
}

func TestUnauthorizedHint(t *testing.T) {
	stubLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	_, err := New().ListOrganizations(context.Background())
	if err == nil || !strings.Contains(err.Error(), "driverforge login") {
		t.Errorf("expected re-login hint on 401, got %v", err)
	}
}

func TestListProjectsFiltersByOrg(t *testing.T) {
	stubLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"myProjects":[
			{"id":"p1","name":"Lights","slug":"lights","platform":"CONTROL4","organizationId":"org-1"},
			{"id":"p2","name":"HVAC","slug":"hvac","platform":"CONTROL4","organizationId":"org-2"},
			{"id":"p3","name":"Pool","slug":"pool","platform":"CONTROL4","organizationId":"org-1"}
		]}}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	projects, err := New().ListProjects(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 || projects[0].ID != "p1" || projects[1].ID != "p3" {
		t.Errorf("expected only org-1 projects, got %+v", projects)
	}
}

func TestCreateProjectSendsInput(t *testing.T) {
	stubLogin(t)
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"createProject":{"id":"p9","name":"New","slug":"new","platform":"CONTROL4","organizationId":"org-1"}}}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	p, err := New().CreateProject(context.Background(), CreateProjectInput{Name: "New", OrganizationID: "org-1"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.ID != "p9" {
		t.Errorf("project = %+v", p)
	}
	// Platform defaults to CONTROL4 and the org id is carried.
	if !strings.Contains(gotBody, "CONTROL4") || !strings.Contains(gotBody, "org-1") {
		t.Errorf("input not sent as expected: %s", gotBody)
	}
}

func TestCreateProjectOverQuotaIsProjectLimit(t *testing.T) {
	stubLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"errors":[{"message":"Your plan does not include canCreateProject","extensions":{"code":"ENTITLEMENT_REQUIRED"}}]}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	_, err := New().CreateProject(context.Background(), CreateProjectInput{Name: "X", OrganizationID: "org-1"})
	if !errors.Is(err, ErrProjectLimit) {
		t.Fatalf("expected ErrProjectLimit, got %v", err)
	}
}

func TestListAllProjectsUnfiltered(t *testing.T) {
	stubLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"myProjects":[
			{"id":"p1","name":"A","slug":"a","platform":"CONTROL4","organizationId":"org-1"},
			{"id":"p2","name":"B","slug":"b","platform":"CONTROL4","organizationId":"org-2"}
		]}}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	all, err := New().ListAllProjects(context.Background())
	if err != nil {
		t.Fatalf("ListAllProjects: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected all projects across orgs, got %+v", all)
	}
}

func TestListOrganizationsNotLoggedIn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DRIVERFORGE_CREDENTIALS_BACKEND", "file")
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	_, err := New().ListOrganizations(context.Background())
	if err != auth.ErrNotLoggedIn {
		t.Errorf("expected ErrNotLoggedIn, got %v", err)
	}
}

// Release reads the deploy stamp from /health/live: a stamped build returns
// its sha-* release, an unstamped one (local dev, release: null) returns
// empty without error, and a non-200 is an error.
func TestRelease(t *testing.T) {
	body := `{"status":"ok","release":"sha-abc1234"}`
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/live" {
			t.Errorf("path = %s, want /health/live", r.URL.Path)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL+"/graphql")

	got, err := New().Release(context.Background())
	if err != nil || got != "sha-abc1234" {
		t.Errorf("Release() = (%q, %v), want (sha-abc1234, nil)", got, err)
	}

	body = `{"status":"ok","release":null}`
	got, err = New().Release(context.Background())
	if err != nil || got != "" {
		t.Errorf("unstamped Release() = (%q, %v), want empty and no error", got, err)
	}

	status = http.StatusServiceUnavailable
	if _, err = New().Release(context.Background()); err == nil {
		t.Error("a non-200 health response should be an error")
	}
}

// The project token is what a driver is identified by, and it arrives on the
// project itself. These pin that the client asks for it: a stub server cannot
// notice the Graph changing shape (that is how the apiKeys(projectId) breakage
// reached users unseen), but it can notice us dropping the field.
func TestMyProjectsAsksForTheToken(t *testing.T) {
	stubLogin(t)
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sent = string(body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"myProjects":[{"id":"p1","name":"P","slug":"p","platform":"c4","organizationId":"o1","token":"drv_abc"}]}}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	projects, err := New().ListProjects(context.Background(), "o1")
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if !strings.Contains(sent, "token") {
		t.Errorf("myProjects query does not request the token: %s", sent)
	}
	if len(projects) != 1 || projects[0].Token != "drv_abc" {
		t.Errorf("token not decoded onto the project: %+v", projects)
	}
}

// Creating a project returns its token in the same response, so init has nothing
// to provision afterwards.
func TestCreateProjectReturnsTheToken(t *testing.T) {
	stubLogin(t)
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sent = string(body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"createProject":{"id":"p9","name":"New","slug":"new","platform":"c4","organizationId":"o1","token":"drv_new"}}}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	p, err := New().CreateProject(context.Background(), CreateProjectInput{Name: "New", OrganizationID: "o1", Platform: "c4"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if !strings.Contains(sent, "token") {
		t.Errorf("createProject mutation does not request the token: %s", sent)
	}
	if p.Token != "drv_new" {
		t.Errorf("Token = %q, want drv_new", p.Token)
	}
}

func TestUpdateDeviceName(t *testing.T) {
	stubLogin(t)

	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"updateDeviceName":{"id":"d1","friendlyName":"main","hostname":"ea5-primary","ip":"192.168.1.10","deviceId":"00:0D:6F:AB:12:34","platform":"CONTROL4","status":"ACTIVE"}}}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	dev, err := New().UpdateDeviceName(context.Background(), "org-1", "d1", "main")
	if err != nil {
		t.Fatalf("UpdateDeviceName: %v", err)
	}
	if dev.FriendlyName != "main" || dev.DisplayName() != "main" {
		t.Errorf("unexpected device: %+v", dev)
	}

	var req struct {
		Variables struct {
			OrganizationID string `json:"organizationId"`
			Input          struct {
				DeviceID     string  `json:"deviceId"`
				FriendlyName *string `json:"friendlyName"`
			} `json:"input"`
		} `json:"variables"`
	}
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if req.Variables.OrganizationID != "org-1" {
		t.Errorf("organizationId = %q, want org-1", req.Variables.OrganizationID)
	}
	// The mutation keys on the ASSOCIATION id, not the hardware deviceId — sending
	// the MAC here would 404 against a device the caller can plainly see.
	if req.Variables.Input.DeviceID != "d1" {
		t.Errorf("input.deviceId = %q, want the association id d1", req.Variables.Input.DeviceID)
	}
	if req.Variables.Input.FriendlyName == nil || *req.Variables.Input.FriendlyName != "main" {
		t.Errorf("input.friendlyName = %v, want \"main\"", req.Variables.Input.FriendlyName)
	}
}

// An empty name must go over the wire as an explicit null: that is how the
// mutation spells "clear the override" and fall back to the hostname.
func TestUpdateDeviceNameClearsWithNull(t *testing.T) {
	stubLogin(t)

	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"updateDeviceName":{"id":"d1","friendlyName":"","hostname":"ea5-primary","deviceId":"00:0D:6F:AB:12:34","status":"ACTIVE"}}}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)

	dev, err := New().UpdateDeviceName(context.Background(), "org-1", "d1", "   ")
	if err != nil {
		t.Fatalf("UpdateDeviceName: %v", err)
	}
	if dev.DisplayName() != "ea5-primary" {
		t.Errorf("DisplayName = %q, want the hostname fallback", dev.DisplayName())
	}
	if !strings.Contains(gotBody, `"friendlyName":null`) {
		t.Errorf("friendlyName not sent as null: %s", gotBody)
	}
}
