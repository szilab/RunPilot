package version

// Version is overridden for release builds with -ldflags -X.
var Version = "1.0.5-dev"

// Deployment identifies a packaged deployment target when a build needs to
// expose target-specific runtime guidance. Ordinary builds leave it empty.
var Deployment = ""
