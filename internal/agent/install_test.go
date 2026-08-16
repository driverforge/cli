package agent

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

func TestHealthDecodesControllerFields(t *testing.T) {
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true,"agentVersion":"0.1.2","controllerVersion":"4.0.0.734549","controllerName":"core1","controllerMac":"00:0F:FF:9C:4F:48","protocol":4,"capabilities":[]}`)
	})
	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.ControllerVersion != "4.0.0.734549" || h.ControllerName != "core1" || h.ControllerMac != "00:0F:FF:9C:4F:48" {
		t.Errorf("controller fields not decoded: %+v", h)
	}
}

func TestProjectTreeParsesHierarchy(t *testing.T) {
	stubLogin(t)
	c := fakeAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/project-tree" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("project-tree should be authed: %q", r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"roots":[
			{"id":"site","name":"Home","type":"site","selectable":false,"children":[
				{"id":"lr","name":"Living Room","type":"room","selectable":true}
			]}
		]}`)
	})
	roots, err := c.ProjectTree(context.Background())
	if err != nil {
		t.Fatalf("ProjectTree: %v", err)
	}
	if len(roots) != 1 || roots[0].Name != "Home" {
		t.Fatalf("roots = %+v", roots)
	}
	if len(roots[0].Children) != 1 || !roots[0].Children[0].Selectable {
		t.Errorf("child room should be selectable: %+v", roots[0].Children)
	}
}

func TestProjectTreeErrorEnvelope(t *testing.T) {
	stubLogin(t)
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `{"error":"director_unreachable","message":"director is offline"}`)
	})
	_, err := c.ProjectTree(context.Background())
	if Code(err) != "director_unreachable" {
		t.Errorf("want director_unreachable code, got %v", err)
	}
}

func TestInstallSendsMultipartAndStreams(t *testing.T) {
	stubLogin(t)
	var gotLocation, gotName, gotDriver, gotHash, gotProp string
	c := fakeAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/install" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotHash = r.Header.Get("X-Driver-Structural-Hash")

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
			t.Fatalf("expected multipart body, got %q (%v)", r.Header.Get("Content-Type"), err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("reading part: %v", err)
			}
			data, _ := io.ReadAll(part)
			switch part.FormName() {
			case "manifest":
				s := string(data)
				if strings.Contains(s, `"Living Room"`) {
					gotLocation = "Living Room"
				}
				if strings.Contains(s, "lr") {
					gotLocation = "lr"
				}
				if strings.Contains(s, "My Driver") {
					gotName = "My Driver"
				}
				if strings.Contains(s, "192.168.1.5") {
					gotProp = "192.168.1.5"
				}
			case "driver":
				gotDriver = string(data)
			}
		}

		// Stream a confirmed install (same protocol as deploy).
		io.WriteString(w, `{"state":"staged","step":"writing driver","terminal":false}
{"state":"confirmed","step":"driver loaded in Living Room","version":"1.0.0","terminal":true}
`)
	})

	var steps []string
	res, terminal, err := c.Install(context.Background(), InstallRequest{
		Filename:       "acme.c4z",
		StructuralHash: "sha256:abc",
		Location:       "lr",
		Name:           "My Driver",
		Properties:     map[string]string{"Host": "192.168.1.5"},
		C4z:            strings.NewReader("C4ZBYTES"),
	}, func(e DeployEvent) { steps = append(steps, e.Step) })
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !res.OK || terminal.State != StateConfirmed || terminal.Version != "1.0.0" {
		t.Errorf("result/terminal wrong: %+v / %+v", res, terminal)
	}
	if gotLocation != "lr" || gotName != "My Driver" || gotDriver != "C4ZBYTES" || gotHash != "sha256:abc" || gotProp != "192.168.1.5" {
		t.Errorf("manifest/payload wrong: loc=%q name=%q driver=%q hash=%q prop=%q", gotLocation, gotName, gotDriver, gotHash, gotProp)
	}
	if len(steps) != 2 || steps[0] != "writing driver" {
		t.Errorf("events not streamed: %v", steps)
	}
}

func TestInstallRejectionIsAgentError(t *testing.T) {
	stubLogin(t)
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"location_invalid","message":"a driver of this type can't be added to that room"}`)
	})
	_, _, err := c.Install(context.Background(), InstallRequest{
		Filename: "acme.c4z", Location: "bad", Name: "X", C4z: strings.NewReader("z"),
	}, nil)
	if Code(err) != "location_invalid" {
		t.Errorf("want location_invalid, got %v", err)
	}
}
