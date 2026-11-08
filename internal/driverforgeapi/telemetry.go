package driverforgeapi

import (
	"context"
	"encoding/json"
)

// telemetryResponseLimit bounds a telemetry read. A page holds up to 1000
// entries, each carrying the driver's full message or payload, so it is far
// larger than the default.
const telemetryResponseLimit = 64 << 20

// TelemetryPage is the paging envelope shared by the logs, events and error
// occurrence lists. Cursor pages backwards: pass it to fetch the next older
// page. WindowMinutes is the look-back the server actually applied, after
// holding the request to the plan's retention.
type TelemetryPage struct {
	HasMore       bool   `json:"hasMore"`
	Cursor        string `json:"cursor"`
	TotalCount    int    `json:"totalCount"`
	WindowMinutes int    `json:"windowMinutes"`
}

// TelemetryQuery selects a page of telemetry for a set of projects.
// TimeRangeMinutes is the look-back from now (the server clamps it to the
// plan); Limit is the page size (the server caps it at 1000).
type TelemetryQuery struct {
	ProjectIDs       []string
	TimeRangeMinutes int
	Limit            int
	Cursor           string
}

func (q TelemetryQuery) vars(filters any) map[string]any {
	v := map[string]any{
		"projectIds":       q.ProjectIDs,
		"timeRangeMinutes": q.TimeRangeMinutes,
		"limit":            q.Limit,
		"filters":          filters,
	}
	if q.Cursor != "" {
		v["cursor"] = q.Cursor
	}
	return v
}

// LogFilters are the server-side log filters. Empty fields are omitted.
type LogFilters struct {
	Levels         []string `json:"levels,omitempty"`
	Controllers    []string `json:"controllers,omitempty"`
	Drivers        []string `json:"drivers,omitempty"`
	DriverVersions []string `json:"driverVersions,omitempty"`
	MessageSearch  string   `json:"messageSearch,omitempty"`
	TraceID        string   `json:"traceId,omitempty"`
}

// EventFilters are the server-side event filters. Empty fields are omitted.
type EventFilters struct {
	Statuses       []string `json:"statuses,omitempty"`
	EventNames     []string `json:"eventNames,omitempty"`
	Controllers    []string `json:"controllers,omitempty"`
	Drivers        []string `json:"drivers,omitempty"`
	DriverVersions []string `json:"driverVersions,omitempty"`
	Implemented    *bool    `json:"implemented,omitempty"`
}

// ErrorFilters are the server-side error occurrence filters.
type ErrorFilters struct {
	Controllers    []string `json:"controllers,omitempty"`
	Drivers        []string `json:"drivers,omitempty"`
	DriverVersions []string `json:"driverVersions,omitempty"`
	TraceID        string   `json:"traceId,omitempty"`
}

// LogEntry is one driver log line. CreatedAt is epoch milliseconds. Args is
// the log call's arguments as the exact JSON the server sent, null when every
// argument was a string; ArgsTruncated is set when the SDK capped them.
type LogEntry struct {
	ID            string          `json:"id"`
	CreatedAt     float64         `json:"createdAt"`
	Level         string          `json:"level"`
	Message       string          `json:"message"`
	Args          json.RawMessage `json:"args"`
	ArgsTruncated bool            `json:"argsTruncated"`
	TraceID       string          `json:"traceId"`
	EventName     string          `json:"eventName"`
	Driver        string          `json:"driver"`
	DriverVersion string          `json:"driverVersion"`
	Controller    string          `json:"controller"`
}

// EventEntry is one driver event. Args and Result are kept as the exact JSON
// the server sent, never re-encoded. Times are epoch milliseconds.
type EventEntry struct {
	TraceID       string          `json:"traceId"`
	Name          string          `json:"name"`
	Args          json.RawMessage `json:"args"`
	Result        json.RawMessage `json:"result"`
	StartedAt     float64         `json:"startedAt"`
	FinishedAt    float64         `json:"finishedAt"`
	Duration      float64         `json:"duration"`
	Status        string          `json:"status"`
	ErrorID       string          `json:"errorId"`
	LastLog       string          `json:"lastLog"`
	Driver        string          `json:"driver"`
	DriverVersion string          `json:"driverVersion"`
	Controller    string          `json:"controller"`
}

// ErrorEntry is one error occurrence. Stacktrace and Args are kept as the
// exact JSON the server sent. CreatedAt is epoch milliseconds.
type ErrorEntry struct {
	ID            string          `json:"id"`
	CreatedAt     float64         `json:"createdAt"`
	Type          string          `json:"type"`
	Value         string          `json:"value"`
	Stacktrace    json.RawMessage `json:"stacktrace"`
	Args          json.RawMessage `json:"args"`
	EventName     string          `json:"eventName"`
	LastLog       string          `json:"lastLog"`
	TraceID       string          `json:"traceId"`
	Fingerprint   string          `json:"fingerprint"`
	Driver        string          `json:"driver"`
	DriverVersion string          `json:"driverVersion"`
	Controller    string          `json:"controller"`
}

const (
	logFields   = `id createdAt level message args argsTruncated traceId eventName driver driverVersion controller`
	eventFields = `traceId name args result startedAt finishedAt duration status errorId lastLog driver driverVersion controller`
	errorFields = `id createdAt type value stacktrace args eventName lastLog traceId fingerprint driver driverVersion controller`
	pageFields  = `hasMore cursor totalCount windowMinutes`
)

const logsQuery = `query($projectIds: [String!], $timeRangeMinutes: Int, $filters: LogFiltersInput, $limit: Int, $cursor: String) {
  logs(projectIds: $projectIds, timeRangeMinutes: $timeRangeMinutes, filters: $filters, limit: $limit, cursor: $cursor) {
    logs { ` + logFields + ` } ` + pageFields + `
  }
}`

// Logs returns one page of logs, newest first.
func (c *Client) Logs(ctx context.Context, q TelemetryQuery, f LogFilters) ([]LogEntry, TelemetryPage, error) {
	var data struct {
		Logs struct {
			TelemetryPage
			Logs []LogEntry `json:"logs"`
		} `json:"logs"`
	}
	if err := c.doLimit(ctx, logsQuery, q.vars(f), &data, telemetryResponseLimit); err != nil {
		return nil, TelemetryPage{}, err
	}
	return data.Logs.Logs, data.Logs.TelemetryPage, nil
}

const eventsQuery = `query($projectIds: [String!], $timeRangeMinutes: Int, $filters: EventFiltersInput, $limit: Int, $cursor: String) {
  paginatedEvents(projectIds: $projectIds, timeRangeMinutes: $timeRangeMinutes, filters: $filters, limit: $limit, cursor: $cursor) {
    events { ` + eventFields + ` } ` + pageFields + `
  }
}`

// Events returns one page of events, newest first.
func (c *Client) Events(ctx context.Context, q TelemetryQuery, f EventFilters) ([]EventEntry, TelemetryPage, error) {
	var data struct {
		Events struct {
			TelemetryPage
			Events []EventEntry `json:"events"`
		} `json:"paginatedEvents"`
	}
	if err := c.doLimit(ctx, eventsQuery, q.vars(f), &data, telemetryResponseLimit); err != nil {
		return nil, TelemetryPage{}, err
	}
	return data.Events.Events, data.Events.TelemetryPage, nil
}

const errorsQuery = `query($projectIds: [String!], $timeRangeMinutes: Int, $filters: ErrorFiltersInput, $limit: Int, $cursor: String) {
  errorOccurrencesPaginated(projectIds: $projectIds, timeRangeMinutes: $timeRangeMinutes, filters: $filters, limit: $limit, cursor: $cursor) {
    errors { ` + errorFields + ` } ` + pageFields + `
  }
}`

// ErrorOccurrences returns one page of individual error occurrences, newest
// first.
func (c *Client) ErrorOccurrences(ctx context.Context, q TelemetryQuery, f ErrorFilters) ([]ErrorEntry, TelemetryPage, error) {
	var data struct {
		Errors struct {
			TelemetryPage
			Errors []ErrorEntry `json:"errors"`
		} `json:"errorOccurrencesPaginated"`
	}
	if err := c.doLimit(ctx, errorsQuery, q.vars(f), &data, telemetryResponseLimit); err != nil {
		return nil, TelemetryPage{}, err
	}
	return data.Errors.Errors, data.Errors.TelemetryPage, nil
}

const logQuery = `query($projectIds: [String!], $id: String!) { log(projectIds: $projectIds, logId: $id) { ` + logFields + ` } }`

// Log fetches one log by id within the projects, or nil when none matches.
func (c *Client) Log(ctx context.Context, projectIDs []string, id string) (*LogEntry, error) {
	var data struct {
		Log *LogEntry `json:"log"`
	}
	err := c.doLimit(ctx, logQuery, map[string]any{"projectIds": projectIDs, "id": id}, &data, telemetryResponseLimit)
	return data.Log, err
}

const eventQuery = `query($projectIds: [String!], $id: String!) { event(projectIds: $projectIds, traceId: $id) { ` + eventFields + ` } }`

// Event fetches one event by its trace id within the projects, or nil when
// none matches.
func (c *Client) Event(ctx context.Context, projectIDs []string, traceID string) (*EventEntry, error) {
	var data struct {
		Event *EventEntry `json:"event"`
	}
	err := c.doLimit(ctx, eventQuery, map[string]any{"projectIds": projectIDs, "id": traceID}, &data, telemetryResponseLimit)
	return data.Event, err
}

const errorQuery = `query($projectIds: [String!], $id: String!) { errorOccurrence(projectIds: $projectIds, errorId: $id) { ` + errorFields + ` } }`

// ErrorOccurrence fetches one error occurrence by id within the projects, or
// nil when none matches.
func (c *Client) ErrorOccurrence(ctx context.Context, projectIDs []string, id string) (*ErrorEntry, error) {
	var data struct {
		Error *ErrorEntry `json:"errorOccurrence"`
	}
	err := c.doLimit(ctx, errorQuery, map[string]any{"projectIds": projectIDs, "id": id}, &data, telemetryResponseLimit)
	return data.Error, err
}

// ErrorGroup is every occurrence of one error in a project, grouped by
// fingerprint. Ref is its quotable reference, <PROJECT-SLUG>-<FINGERPRINT>.
// Times are epoch milliseconds; LatestStacktrace is the server's JSON.
type ErrorGroup struct {
	Fingerprint         string          `json:"fingerprint"`
	Ref                 string          `json:"ref"`
	Type                string          `json:"type"`
	Value               string          `json:"value"`
	Count               int             `json:"count"`
	FirstSeen           float64         `json:"firstSeen"`
	LastSeen            float64         `json:"lastSeen"`
	AffectedVersions    []string        `json:"affectedVersions"`
	AffectedControllers []string        `json:"affectedControllers"`
	AffectedDrivers     []string        `json:"affectedDrivers"`
	LatestEventName     string          `json:"latestEventName"`
	LatestStacktrace    json.RawMessage `json:"latestStacktrace"`
	LatestOccurrenceID  string          `json:"latestOccurrenceId"`
}

const errorGroupQuery = `query($projectId: String!, $fingerprint: String!) {
  errorGroupByFingerprint(projectId: $projectId, fingerprint: $fingerprint) {
    fingerprint ref type value count firstSeen lastSeen affectedVersions affectedControllers affectedDrivers latestEventName latestStacktrace latestOccurrenceId
  }
}`

// ErrorGroup fetches the project's error group with the fingerprint, however
// old, or nil when there's none.
func (c *Client) ErrorGroup(ctx context.Context, projectID, fingerprint string) (*ErrorGroup, error) {
	var data struct {
		Group *ErrorGroup `json:"errorGroupByFingerprint"`
	}
	err := c.doLimit(ctx, errorGroupQuery, map[string]any{"projectId": projectID, "fingerprint": fingerprint}, &data, telemetryResponseLimit)
	return data.Group, err
}
