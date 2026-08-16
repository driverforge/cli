package metrics

// BusinessEvent is a product-analytics event (sent to Mixpanel). Name is the
// event name (e.g. "build"); Properties carry contextual dimensions.
type BusinessEvent struct {
	Name       string
	Properties map[string]any
}
