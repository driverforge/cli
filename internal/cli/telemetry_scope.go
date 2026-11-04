package cli

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/driverforge/cli/internal/deployrecord"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/driverforgeconfig"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/telemetryread"
)

// Environment variables that set the telemetry commands' scope, between the
// flags and the repo config in precedence.
const (
	envOrg     = "DRIVERFORGE_ORG"
	envProject = "DRIVERFORGE_PROJECT"
	envDevice  = "DRIVERFORGE_DEVICE"
)

// Version selectors --version accepts besides a literal version.
const (
	versionCurrent = "current" // what this CLI last deployed to the device
	versionLatest  = "latest"  // the highest version seen in telemetry
)

// scopeFlags are the scope a telemetry command was asked for: flag values,
// "" when unset.
type scopeFlags struct {
	org, project, device, version string
}

// telemetryScope is the resolved scope a telemetry command reads.
type telemetryScope struct {
	org     driverforgeapi.Organization
	project driverforgeapi.Project
	device  *scopeDevice // nil when the command reads every device (get)
	version string       // "" for any version
	devices []driverforgeapi.Device
}

// scopeDevice is the resolved controller.
type scopeDevice struct {
	id   string // graph device id: the deploy record's key
	mac  string // hardware id: what telemetry records as the controller
	name string
}

// names maps a controller's hardware id to its display name, for entries.
func (s *telemetryScope) names(controller string) string {
	for _, d := range s.devices {
		if strings.EqualFold(d.DeviceID, controller) {
			return d.DisplayName()
		}
	}
	return ""
}

// scopeInputs applies flag > environment > repo config precedence. The global
// context (the selected org and device) is the last resort, applied during
// resolution because it needs the resolved org.
func scopeInputs(f scopeFlags) scopeFlags {
	pick := func(flag, env string) string {
		if flag != "" {
			return flag
		}
		return os.Getenv(env)
	}
	in := scopeFlags{
		org:     pick(f.org, envOrg),
		project: pick(f.project, envProject),
		device:  pick(f.device, envDevice),
		version: f.version,
	}
	if in.org != "" && in.project != "" {
		return in
	}
	if p, err := project.Locate(flagProjectDir); err == nil {
		if cfg, err := driverforgeconfig.Load(p.Dir); err == nil && cfg != nil {
			if in.org == "" {
				in.org = cfg.Org
			}
			if in.project == "" {
				in.project = cfg.Project.Slug
			}
		}
	}
	return in
}

// resolveScope turns the requested scope into graph identities. withDevice
// resolves a device (the query commands); without it the scope is the whole
// project (get). It never prompts: anything missing or ambiguous is an error
// naming the flag or variable that settles it.
func resolveScope(ctx context.Context, api *driverforgeapi.Client, f scopeFlags, withDevice bool) (*telemetryScope, error) {
	in := scopeInputs(f)
	if in.org == "" {
		if cur, err := orgctx.Current(); err == nil {
			in.org = cur.ID
		}
	}
	if in.project == "" {
		return nil, newCmdErr(codeContextUnresolved, "No project could be determined.",
			"Run from a driver repo, pass --project, or set "+envProject+".").
			with("missing", []string{"project"})
	}

	orgs, err := api.ListOrganizations(ctx)
	if err != nil {
		return nil, err
	}
	s := &telemetryScope{}
	if in.org != "" {
		o, err := matchOrg(orgs, in.org)
		if err != nil {
			return nil, err
		}
		s.org = o
	}

	projects, err := api.ListAllProjects(ctx)
	if err != nil {
		return nil, err
	}
	if s.project, err = matchProject(projects, s.org.ID, in.project); err != nil {
		return nil, err
	}
	if s.org.ID == "" {
		// The project named its org.
		for _, o := range orgs {
			if o.ID == s.project.OrganizationID {
				s.org = o
			}
		}
	}

	if s.devices, err = api.ListDevices(ctx, s.org.ID); err != nil {
		return nil, err
	}
	if withDevice {
		if s.device, err = resolveScopeDevice(s.devices, s.org.ID, in.device); err != nil {
			return nil, err
		}
	}
	if err := s.resolveVersion(ctx, api, in.version); err != nil {
		return nil, err
	}
	return s, nil
}

func matchOrg(orgs []driverforgeapi.Organization, arg string) (driverforgeapi.Organization, error) {
	var byName []driverforgeapi.Organization
	for _, o := range orgs {
		if o.ID == arg || o.Slug == arg {
			return o, nil
		}
		if strings.EqualFold(o.Name, arg) {
			byName = append(byName, o)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return driverforgeapi.Organization{}, newCmdErr(codeContextUnresolved,
			"No organization you belong to matches "+strconv.Quote(arg)+".",
			"Pass --org with a slug from `driverforge org list`, or set "+envOrg+".").
			with("org", arg)
	}
	cands := make([]string, len(byName))
	for i, o := range byName {
		cands[i] = o.Slug
	}
	return driverforgeapi.Organization{}, newCmdErr(codeContextAmbiguous,
		"More than one organization is named "+strconv.Quote(arg)+".",
		"Pass --org with one of the slugs listed.").
		with("field", "org").with("candidates", cands)
}

// matchProject finds the project by id or slug, then by name, within the org
// when it is known.
func matchProject(projects []driverforgeapi.Project, orgID, arg string) (driverforgeapi.Project, error) {
	var exact, byName []driverforgeapi.Project
	for _, p := range projects {
		if orgID != "" && p.OrganizationID != orgID {
			continue
		}
		switch {
		case p.ID == arg || p.Slug == arg:
			exact = append(exact, p)
		case strings.EqualFold(p.Name, arg):
			byName = append(byName, p)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = byName
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return driverforgeapi.Project{}, newCmdErr(codeProjectNotFound,
			"No project you can access matches "+strconv.Quote(arg)+".",
			"Check --project (or "+envProject+") and --org; your account must be a member of the project.").
			with("project", arg)
	}
	cands := make([]string, len(matches))
	for i, p := range matches {
		cands[i] = p.Slug
	}
	return driverforgeapi.Project{}, newCmdErr(codeContextAmbiguous,
		"More than one project matches "+strconv.Quote(arg)+".",
		"Pass --org to choose the organization, or --project with a project id.").
		with("field", "project").with("candidates", cands)
}

// resolveScopeDevice picks the controller: the --device/env value, else the
// device selected with `driverforge device select` for this org, else the org's
// only device. It never chooses one device from several.
func resolveScopeDevice(devices []driverforgeapi.Device, orgID, arg string) (*scopeDevice, error) {
	toScope := func(d driverforgeapi.Device) *scopeDevice {
		return &scopeDevice{id: d.ID, mac: d.DeviceID, name: d.DisplayName()}
	}
	if arg != "" {
		var matches []driverforgeapi.Device
		for _, d := range devices {
			if d.ID == arg || strings.EqualFold(d.DeviceID, arg) ||
				strings.EqualFold(d.Hostname, arg) || strings.EqualFold(d.FriendlyName, arg) {
				matches = append(matches, d)
			}
		}
		switch len(matches) {
		case 1:
			return toScope(matches[0]), nil
		case 0:
			return nil, newCmdErr(codeDeviceNotFound,
				"No device in this organization matches "+strconv.Quote(arg)+".",
				"Run `driverforge device list` for valid --device values.").
				with("device", arg)
		}
		return nil, ambiguousDevice(matches)
	}
	if cur, err := orgctx.CurrentDevice(); err == nil && cur.OrgID == orgID {
		for _, d := range devices {
			if d.ID == cur.ID {
				return toScope(d), nil
			}
		}
	}
	switch len(devices) {
	case 1:
		return toScope(devices[0]), nil
	case 0:
		return nil, newCmdErr(codeContextUnresolved, "No device could be determined.",
			"Run `driverforge device select`, pass --device, or set "+envDevice+".").
			with("missing", []string{"device"})
	}
	return nil, ambiguousDevice(devices)
}

func ambiguousDevice(devices []driverforgeapi.Device) error {
	cands := make([]map[string]string, len(devices))
	for i, d := range devices {
		cands[i] = map[string]string{"id": d.DeviceID, "name": d.DisplayName()}
	}
	return newCmdErr(codeContextAmbiguous, "More than one device could be meant.",
		"Run `driverforge device select`, or pass --device with one of the ids listed.").
		with("field", "device").with("candidates", cands)
}

// resolveVersion settles --version: a literal, the version this CLI last
// deployed to the device, or the highest version in the device's telemetry.
func (s *telemetryScope) resolveVersion(ctx context.Context, api *driverforgeapi.Client, v string) error {
	switch v {
	case "":
		return nil
	case versionCurrent:
		if s.device == nil {
			return newCmdErr(codeUsage, "--version current needs a device.", "Pass --version with a number.")
		}
		rec, ok := deployrecord.Latest(s.project.Slug, s.device.id)
		if !ok {
			return newCmdErr(codeNoDeployRecorded,
				"This machine has no record of deploying "+s.project.Slug+" to "+s.device.name+".",
				"Pass --version <n>, or run `driverforge deploy` from this machine first.").
				with("project", s.project.Slug).with("device", s.device.mac)
		}
		s.version = rec.Version
		return nil
	case versionLatest:
		latest, err := s.latestVersion(ctx, api)
		if err != nil {
			return err
		}
		if latest == "" {
			return newCmdErr(codeContextUnresolved, "No telemetry from this device carries a driver version yet.",
				"Pass --version <n>, or deploy with a current SDK so entries are versioned.").
				with("missing", []string{"version"})
		}
		s.version = latest
		return nil
	}
	s.version = v
	return nil
}

// latestVersion is the highest driver version among the newest log, error and
// event from the device. Versions compare numerically when both are integers
// (Control4 versions are), otherwise as text.
func (s *telemetryScope) latestVersion(ctx context.Context, api *driverforgeapi.Client) (string, error) {
	var best string
	for _, kind := range []telemetryread.Kind{telemetryread.KindLog, telemetryread.KindError, telemetryread.KindEvent} {
		f := telemetryread.Filter{Kind: kind}
		if s.device != nil {
			f.Controller = s.device.mac
		}
		src := telemetryread.GraphSource{API: api, Filter: f, ProjectIDs: []string{s.project.ID}, TimeRangeMinutes: maxLookbackMinutes}
		page, err := src.Page(ctx, "", 1)
		if err != nil {
			return "", err
		}
		for _, e := range page.Entries {
			if v := deref(e.DriverVersion); v != "" && versionLess(best, v) {
				best = v
			}
		}
	}
	return best, nil
}

func versionLess(a, b string) bool {
	if a == "" {
		return true
	}
	ai, aerr := strconv.ParseInt(a, 10, 64)
	bi, berr := strconv.ParseInt(b, 10, 64)
	if aerr == nil && berr == nil {
		return ai < bi
	}
	return a < b
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
