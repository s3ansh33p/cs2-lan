package cs2

// Docker container conventions for the CS2 server image.
const (
	containerPrefix = "cs2-"
	demoPath        = "/home/steam/cs2-dedicated/game/csgo/replays/"
)

func (Game) ContainerPrefix() string { return containerPrefix }
func (Game) DemoPath() string        { return demoPath }
