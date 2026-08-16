package metrics

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// mixpanelSink delivers business events to Mixpanel's /track endpoint. It is
// inert (no-op) when no project token is configured.
type mixpanelSink struct {
	token string
	url   string
}

func (s *mixpanelSink) Name() string { return "mixpanel" }

func (s *mixpanelSink) Send(ctx context.Context, id Identity, events []BusinessEvent) error {
	if s.token == "" || s.url == "" || len(events) == 0 {
		return nil
	}

	// Identity follows Mixpanel's simplified ID merge: $device_id always carries
	// the install's anonymous id; when signed in, $user_id carries the account
	// subject (the same id the platform's server-side analytics person uses) and
	// becomes the distinct_id, so CLI events land on that person. Signed-out
	// events stay on the "$device:" person until a later sign-in merges them.
	distinct := "$device:" + id.DeviceID
	if id.UserID != "" {
		distinct = id.UserID
	}

	// Mixpanel /track accepts a base64-encoded JSON array of {event, properties}.
	payload := make([]map[string]any, 0, len(events))
	for _, e := range events {
		props := map[string]any{
			"token":       s.token,
			"distinct_id": distinct,
			"$device_id":  id.DeviceID,
			// source segments CLI traffic from the web apps sharing the project.
			"source": "cli",
		}
		if id.UserID != "" {
			props["$user_id"] = id.UserID
		}
		if id.OrgID != "" {
			// The platform's analytics library attaches orgs under this exact
			// key (its Mixpanel group key), so org-level views span web + CLI.
			props["organization"] = id.OrgID
		}
		for k, v := range e.Properties {
			props[k] = v
		}
		payload = append(payload, map[string]any{
			"event":      e.Name,
			"properties": props,
		})
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	form := url.Values{}
	form.Set("data", base64.StdEncoding.EncodeToString(body))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/plain")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("mixpanel: HTTP %d", resp.StatusCode)
	}
	return nil
}
