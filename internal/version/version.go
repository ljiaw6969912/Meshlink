package version

var (
	Version   = "0.1.0-dev"
	BuildTime = "dev"
)

func Display() string {
	if BuildTime == "" || BuildTime == "dev" {
		return Version
	}
	return Version + " (" + BuildTime + ")"
}
