package c4z

import "fmt"

// This file holds the anticipated, user-caused build failures as distinct error
// types. The package stays UI-agnostic — it never imports the CLI's clierr — so
// the CLI maps these to friendly UserErrors at its boundary (see cli.buildErr),
// while other consumers can match them with errors.As.

// ConfigNotFoundError is returned when a build configuration is requested but its
// config.<Configuration>.lua override is missing from the source tree.
type ConfigNotFoundError struct {
	Configuration string
	Dir           string
}

func (e *ConfigNotFoundError) Error() string {
	return fmt.Sprintf("config.%s.lua not found in %s", e.Configuration, e.Dir)
}

// WarningsAsErrorsError is returned when a build emitted warnings while
// Options.WarningsAsErrors was set. The warnings themselves were already
// reported as StepWarn events; this failure stops the pipeline before any
// artifact is packaged.
type WarningsAsErrorsError struct {
	Count int
}

func (e *WarningsAsErrorsError) Error() string {
	if e.Count == 1 {
		return "1 warning treated as an error"
	}
	return fmt.Sprintf("%d warnings treated as errors", e.Count)
}

// UnsupportedDriverTypeError is returned when the manifest declares a driver type
// other than the supported "c4z".
type UnsupportedDriverTypeError struct {
	Type string
}

func (e *UnsupportedDriverTypeError) Error() string {
	return fmt.Sprintf("driver type %q is not supported (only %q)", e.Type, "c4z")
}

// NoEncryptableScriptError is returned when --encrypt requests encryption but
// driver.xml has no <script file="..."> tag to mark, so the build would
// otherwise silently ship plaintext despite the explicit ask.
type NoEncryptableScriptError struct{}

func (e *NoEncryptableScriptError) Error() string {
	return `encryption was requested but driver.xml has no <script file="..."> tag to encrypt`
}

// NoScriptFileError is returned when a flag needs the driver's declared
// <script file="..."> (e.g. --allow-execute) but driver.xml has none.
type NoScriptFileError struct {
	Flag string
}

func (e *NoScriptFileError) Error() string {
	return fmt.Sprintf(`%s requires a <script file="..."> tag in driver.xml, but none was found`, e.Flag)
}
