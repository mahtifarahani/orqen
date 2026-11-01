package sysprobe

import (
	"bytes"
	"context"
	"encoding/xml"
	"strconv"
)

const LaunchdLabel = "dev.orqen.server"

// LaunchdPlan is displayed text only: Orqen never writes into ~/Library/LaunchAgents or runs launchctl write verbs.
type LaunchdPlan struct {
	PlistPath         string
	PlistXML          string
	InstallCommands   []string
	UninstallCommands []string
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// BuildLaunchdPlan generates the plist for a user agent that runs `<exe> serve`.
func BuildLaunchdPlan(exe, dataDir, childPath string) LaunchdPlan {
	xmlText := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + LaunchdLabel + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + esc(exe) + `</string>
    <string>serve</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>ORQEN_DATA_DIR</key><string>` + esc(dataDir) + `</string>
    <key>ORQEN_CATHERD_PATH</key><string>` + esc(childPath) + `</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><false/>
</dict>
</plist>
`
	path := "~/Library/LaunchAgents/" + LaunchdLabel + ".plist"
	return LaunchdPlan{
		PlistPath: path,
		PlistXML:  xmlText,
		InstallCommands: []string{
			"mkdir -p ~/Library/LaunchAgents",
			"# save the XML above as " + path,
			"launchctl bootstrap gui/$(id -u) " + path,
		},
		UninstallCommands: []string{
			"launchctl bootout gui/$(id -u)/" + LaunchdLabel,
			"rm " + path,
		},
	}
}

func launchdCheck(ctx context.Context, d Deps, in Inputs) []Check {
	return []Check{timed(func() Check {
		c := Check{ID: "launchd.start_at_login", Group: "service", Blocks: []string{"start_at_login"}}
		if !in.LaunchdOptIn {
			c.Status, c.Detail = Skipped, "start at login was not requested"
			return c
		}
		if !d.FS.Exists(in.LaunchdPlist) {
			c.Status, c.FixCode, c.Detail = Warn, "launchd_not_installed", "the launchd agent file does not exist"
			c.FixCommand = []string{"see the plan shown in the launchd panel"}
			return c
		}
		if _, exit, err := d.Runner.Run(ctx, []string{"launchctl", "print", "gui/" + strconv.Itoa(in.UID) + "/" + LaunchdLabel}, runEnv(in)); err != nil || exit != 0 {
			c.Status, c.FixCode, c.Detail = Warn, "launchd_not_loaded", "the agent file exists but launchd has not loaded it"
			c.FixCommand = []string{"launchctl bootstrap gui/$(id -u) " + in.LaunchdPlist}
			return c
		}
		c.Status, c.Detail = Pass, "the agent is loaded"
		return c
	})}
}
