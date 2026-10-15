package trusttunnel

import "runtime"

// Platform returns the platform string the official clients put in front of the
// user agent.
func Platform() string {
	switch runtime.GOOS {
	case "darwin":
		return "Mac"
	case "windows":
		return "Windows"
	case "android":
		return "Android"
	case "ios":
		return "iOS"
	default:
		return "Linux"
	}
}

// UserAgent is the user agent of a TCP CONNECT: "<platform> <app>".
func UserAgent(app string) string {
	return Platform() + " " + app
}

// UserAgentUDP is the user agent of the UDP multiplex stream.
func UserAgentUDP() string {
	return Platform() + " " + AuthorityUDP
}

// UserAgentCheck is the user agent of a health check; the official client sends
// the literal application "unknown" there.
func UserAgentCheck() string {
	return Platform() + " unknown"
}
