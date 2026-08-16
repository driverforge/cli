package c4z

// artifactName derives the configuration-specific artifact base name. A default
// build (no configuration) keeps the naked name; a named configuration is
// suffixed so its build coexists with the default, unless the caller opted out
// (noSuffix) to produce a release artifact under the naked name.
func artifactName(name, configuration string, noSuffix bool) string {
	if configuration == "" || noSuffix {
		return name
	}
	return name + "-" + configuration
}

// displayNameSuffix is appended to the driver.xml device <name> so a named
// configuration's variant is distinguishable in Composer. A default build (no
// configuration) or a suffix opt-out (noSuffix) leaves the name unchanged.
func displayNameSuffix(configuration string, noSuffix bool) string {
	if configuration == "" || noSuffix {
		return ""
	}
	return " (" + configuration + ")"
}
