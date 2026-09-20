package productflags

import (
	"os"
	"strings"
)

const OfficialHubMVPEnv = "MESHLINK_ENABLE_OFFICIAL_HUB_MVP"

func OfficialHubMVPEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(OfficialHubMVPEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
