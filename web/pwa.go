package web

// PWARootFiles are the root-level build outputs a browser fetches before any
// session exists: the progressive-web-app manifest and service worker, plus the
// icons the manifest references. None of them carry user data.
//
// The server exempts exactly these paths from session checks. Declaring them
// next to the files they name, rather than restating them in the auth
// middleware, means renaming an icon cannot silently make the Web UI
// uninstallable: the paths and the exemption move together, and
// TestPWARootFilesArePublished fails the moment one stops shipping.
var PWARootFiles = []string{
	"/sw.js",
	"/site.webmanifest",
	"/favicon.svg",
	"/favicon-32x32.png",
	"/apple-touch-icon.png",
	"/icon-192.png",
	"/icon-512.png",
}
