package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/driverforge/cli/internal/sanitize"
)

// CapabilityBootstrap is advertised in /healthz capabilities by agents that
// support first-deploy bootstrap — i.e. they serve GET /project-tree and accept
// POST /install. The CLI only offers the install wizard when it is present.
const CapabilityBootstrap = "bootstrap"

// ProjectNode is one location in the director's project hierarchy (site →
// building → floor → room). Selectable marks a node a driver may actually be
// installed into — typically rooms; the rest are navigation-only containers. The
// shape mirrors ui.TreeNode so the CLI can map it straight into the tree picker.
type ProjectNode struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Type       string        `json:"type"` // "site","building","floor","room",…
	Selectable bool          `json:"selectable"`
	Children   []ProjectNode `json:"children,omitempty"`
}

// ProjectTree fetches the director's location hierarchy (GET /project-tree) so
// the CLI can prompt for an install location. The response envelope is
// {"roots":[…]}.
func (c *Client) ProjectTree(ctx context.Context) ([]ProjectNode, error) {
	req, err := c.authed(ctx, http.MethodGet, c.baseURL+"/project-tree", "", nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		Roots []ProjectNode `json:"roots"`
	}
	if err := c.do(req, &env); err != nil {
		return nil, err
	}
	scrubNodes(env.Roots)
	return env.Roots, nil
}

// scrubNodes strips terminal control sequences from the display names in the
// project tree — they render straight into the location picker.
func scrubNodes(nodes []ProjectNode) {
	for i := range nodes {
		nodes[i].Name = sanitize.Terminal(nodes[i].Name)
		scrubNodes(nodes[i].Children)
	}
}

// InstallRequest carries everything the agent needs to add a driver to the
// project for the first time: the built .c4z plus where it goes (Location, a
// ProjectNode id), the installed driver Name, and the property values to seed.
type InstallRequest struct {
	Filename       string
	StructuralHash string
	Location       string            // chosen ProjectNode id
	Name           string            // name for the installed driver instance
	Properties     map[string]string // property name → value (from the form)
	C4z            io.Reader
}

// installManifest is the JSON metadata part of the install multipart body.
type installManifest struct {
	Filename       string            `json:"filename"`
	StructuralHash string            `json:"structuralHash,omitempty"`
	Location       string            `json:"location"`
	Name           string            `json:"name"`
	Properties     map[string]string `json:"properties,omitempty"`
}

// Install adds the driver to the project and loads it, streaming the same NDJSON
// progress as a deploy (the install IS a deploy that also creates the device and
// seeds properties). onEvent (may be nil) is invoked per event until the first
// Terminal one; it returns the final DeployResult and that terminal event.
//
// The request is multipart/form-data: a JSON `manifest` part (location/name/
// properties) and the `driver` part (the .c4z bytes). Like the deploy stream it
// is bounded only by ctx, not the client's 30s timeout.
func (c *Client) Install(ctx context.Context, in InstallRequest, onEvent func(DeployEvent)) (DeployResult, DeployEvent, error) {
	body, contentType, err := buildInstallBody(in)
	if err != nil {
		return DeployResult{}, DeployEvent{}, err
	}

	req, err := c.authed(ctx, http.MethodPost, c.baseURL+"/install", in.Filename, body)
	if err != nil {
		return DeployResult{}, DeployEvent{}, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/x-ndjson")
	if in.StructuralHash != "" {
		req.Header.Set("X-Driver-Structural-Hash", in.StructuralHash)
	}

	resp, err := c.streamingHTTP().Do(req)
	if err != nil {
		return DeployResult{}, DeployEvent{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return DeployResult{}, DeployEvent{}, parseErrorEnvelope(resp.StatusCode, data)
	}
	return readDeployStream(resp.Body, in.Filename, onEvent)
}

// buildInstallBody assembles the multipart body (manifest JSON + driver bytes).
// The .c4z is buffered (drivers are small); the manifest is written first so the
// agent can read metadata before the payload.
func buildInstallBody(in InstallRequest) (io.Reader, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	manifest := installManifest{
		Filename:       in.Filename,
		StructuralHash: in.StructuralHash,
		Location:       in.Location,
		Name:           in.Name,
		Properties:     in.Properties,
	}
	mw, err := w.CreateFormField("manifest")
	if err != nil {
		return nil, "", err
	}
	if err := json.NewEncoder(mw).Encode(manifest); err != nil {
		return nil, "", err
	}

	dw, err := w.CreateFormFile("driver", in.Filename)
	if err != nil {
		return nil, "", err
	}
	if in.C4z != nil {
		if _, err := io.Copy(dw, in.C4z); err != nil {
			return nil, "", fmt.Errorf("agent: reading driver bytes: %w", err)
		}
	}

	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}
