package azprofile

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/neverprepared/azprofile/internal/ui"
)

const (
	CronTagPrefix    = "# azprofile-refresh"
	CronPIMTagPrefix = "# azprofile-pim"
)

const (
	DefaultCronSchedule    = "13 * * * *"
	DefaultPIMCronSchedule = "30 8 * * *"
	DefaultPIMDuration     = 480
	DefaultPIMReason       = "cron"
)

// homeExpr is the shell expression that resolves at cron run-time. Cron's job
// environment doesn't carry WORKSPACE_HOME automatically; CronInstall/CronPIMInstall
// add a top-of-crontab `WORKSPACE_HOME=<value>` line when the var is set in the
// caller's shell so this expansion picks it up.
const homeExpr = `${WORKSPACE_HOME:-$HOME}`

func cronTag(profile string) string {
	if profile == "" {
		profile = "all"
	}
	return CronTagPrefix + ":" + profile
}

func cronPIMTag(profile string) string {
	return CronPIMTagPrefix + ":" + profile
}

func readCrontab() string {
	out, err := exec.Command("crontab", "-l").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func writeCrontab(content string) error {
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(content)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func filterOut(content, substr string) string {
	if content == "" {
		return ""
	}
	var kept []string
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, substr) {
			kept = append(kept, line)
		}
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n")
}

// ensureWorkspaceHomeEnv inserts or updates `WORKSPACE_HOME=<value>` at the top
// of the crontab when WORKSPACE_HOME is set in the caller's shell. Returns the
// (possibly-modified) content and a bool indicating whether a change was made.
// No-op when WORKSPACE_HOME is unset.
func ensureWorkspaceHomeEnv(content string) (string, bool) {
	wh := os.Getenv("WORKSPACE_HOME")
	if wh == "" {
		return content, false
	}
	want := "WORKSPACE_HOME=" + wh
	found := false
	changed := false
	var out []string
	for _, l := range strings.Split(content, "\n") {
		if strings.HasPrefix(l, "WORKSPACE_HOME=") {
			found = true
			if l != want {
				changed = true
				out = append(out, want)
			} else {
				out = append(out, l)
			}
			continue
		}
		out = append(out, l)
	}
	if !found {
		rest := strings.TrimLeft(content, "\n")
		if rest == "" {
			return want, true
		}
		return want + "\n" + rest, true
	}
	if !changed {
		return content, false
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n"), true
}

// appendCronLine combines a base crontab (already filtered of the target tag)
// and a new cron line into a single payload ready to feed to `crontab -`.
func appendCronLine(base, cronLine string) string {
	if base == "" {
		return cronLine + "\n"
	}
	return strings.TrimRight(base, "\n") + "\n" + cronLine + "\n"
}

// shellQuote wraps s in single quotes for safe inclusion in a /bin/sh command,
// escaping internal single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// displayHome returns WORKSPACE_HOME if set in this process's env, otherwise
// the value of Home(). Used for friendly output paths only — actual cron lines
// use the runtime shell expression.
func displayHome() string {
	if wh := os.Getenv("WORKSPACE_HOME"); wh != "" {
		return wh
	}
	return Home()
}

func CronInstall(profile, schedule string) error {
	if schedule == "" {
		schedule = DefaultCronSchedule
	}
	tag := cronTag(profile)

	refreshArgs := ""
	label := "all profiles"
	if profile != "" {
		if err := ValidateProfileName(profile); err != nil {
			return err
		}
		if fi, err := os.Stat(ProfilePath(profile)); err != nil || !fi.IsDir() {
			return fmt.Errorf("Profile '%s' not found. Run: azprofile init %s", profile, profile)
		}
		refreshArgs = " " + shellQuote(profile)
		label = "profile '" + profile + "'"
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}

	cronLine := fmt.Sprintf(
		`%s AZPROFILE_HOME="%s" %s refresh%s >> "%s/.azure-profiles/refresh.log" 2>&1 %s`,
		schedule, homeExpr, exe, refreshArgs, homeExpr, tag,
	)

	base := filterOut(readCrontab(), tag)
	base, whChanged := ensureWorkspaceHomeEnv(base)
	if err := writeCrontab(appendCronLine(base, cronLine)); err != nil {
		return err
	}

	if whChanged {
		fmt.Printf("%s%s%s WORKSPACE_HOME=%s set in crontab\n",
			ui.Green, ui.Check, ui.NC, os.Getenv("WORKSPACE_HOME"))
	}
	fmt.Printf("%s%s%s Cron installed for %s%s%s: %s%s%s\n",
		ui.Green, ui.Check, ui.NC, ui.Bold, label, ui.NC, ui.Dim, schedule, ui.NC)
	fmt.Printf("%s  Log: %s/.azure-profiles/refresh.log%s\n", ui.Dim, displayHome(), ui.NC)
	return nil
}

// PIMCronOpts configures the activate invocation baked into a pim cron entry.
type PIMCronOpts struct {
	All          bool   // when true, omit positional roles and use `pim activate --all --yes`
	Type         string // optional --type filter (resource/role/group/all)
	Role         string // optional --role filter (works with --all or as disambiguator)
	Duration     int
	Reason       string
	TicketSystem string
	TicketNumber string
}

func CronPIMInstall(profile, schedule string, roles []string, opts PIMCronOpts) error {
	if profile == "" {
		return fmt.Errorf("profile required for pim cron")
	}
	if err := ValidateProfileName(profile); err != nil {
		return err
	}
	if opts.All && len(roles) > 0 {
		return fmt.Errorf("--all is mutually exclusive with positional role names")
	}
	if !opts.All && len(roles) == 0 {
		return fmt.Errorf("at least one role required, or pass --all")
	}
	if fi, err := os.Stat(ProfilePath(profile)); err != nil || !fi.IsDir() {
		return fmt.Errorf("Profile '%s' not found. Run: azprofile init %s", profile, profile)
	}
	if schedule == "" {
		schedule = DefaultPIMCronSchedule
	}
	if opts.Duration == 0 {
		opts.Duration = DefaultPIMDuration
	}
	if opts.Reason == "" {
		opts.Reason = DefaultPIMReason
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}

	var activateArgs strings.Builder
	if opts.All {
		activateArgs.WriteString(" --all --yes")
		if opts.Type != "" && opts.Type != "all" {
			activateArgs.WriteString(" --type ")
			activateArgs.WriteString(shellQuote(opts.Type))
		}
		if opts.Role != "" {
			activateArgs.WriteString(" --role ")
			activateArgs.WriteString(shellQuote(opts.Role))
		}
	} else {
		for _, r := range roles {
			activateArgs.WriteString(" ")
			activateArgs.WriteString(shellQuote(r))
		}
	}

	extra := ""
	if opts.TicketSystem != "" {
		extra += " --ticket-system " + shellQuote(opts.TicketSystem)
	}
	if opts.TicketNumber != "" {
		extra += " --ticket-number " + shellQuote(opts.TicketNumber)
	}

	tag := cronPIMTag(profile)
	quotedProfile := shellQuote(profile)
	// Wrap the refresh+activate chain in `{ ...; }` so the redirect captures
	// stdout/stderr from BOTH commands. Without the braces, `>> pim.log 2>&1`
	// only attaches to the rightmost simple command (the pim activate call),
	// and a failing refresh leaves no diagnostic trail.
	cronLine := fmt.Sprintf(
		`%s AZPROFILE_HOME="%s" AZURE_CONFIG_DIR="%s/.azure-profiles/%s" { %s refresh %s && %s pim activate%s --reason %s -d %d%s ; } >> "%s/.azure-profiles/pim.log" 2>&1 %s`,
		schedule, homeExpr,
		homeExpr, profile,
		exe, quotedProfile,
		exe, activateArgs.String(),
		shellQuote(opts.Reason), opts.Duration, extra,
		homeExpr, tag,
	)

	base := filterOut(readCrontab(), tag)
	base, whChanged := ensureWorkspaceHomeEnv(base)
	if err := writeCrontab(appendCronLine(base, cronLine)); err != nil {
		return err
	}

	if whChanged {
		fmt.Printf("%s%s%s WORKSPACE_HOME=%s set in crontab\n",
			ui.Green, ui.Check, ui.NC, os.Getenv("WORKSPACE_HOME"))
	}
	rolesDesc := strings.Join(roles, ", ")
	if opts.All {
		rolesDesc = "all eligible"
		if opts.Type != "" && opts.Type != "all" {
			rolesDesc += " (" + opts.Type + ")"
		}
		if opts.Role != "" {
			rolesDesc += " matching '" + opts.Role + "'"
		}
	}
	fmt.Printf("%s%s%s PIM cron installed for profile %s'%s'%s, roles: %s%s%s schedule: %s%s%s\n",
		ui.Green, ui.Check, ui.NC,
		ui.Bold, profile, ui.NC,
		ui.Bold, rolesDesc, ui.NC,
		ui.Dim, schedule, ui.NC)
	fmt.Printf("%s  Log: %s/.azure-profiles/pim.log%s\n", ui.Dim, displayHome(), ui.NC)
	return nil
}

func CronRemove(profile string) error {
	tag := CronTagPrefix
	if profile != "" {
		tag = cronTag(profile)
	}
	filtered := filterOut(readCrontab(), tag)
	if filtered == "" {
		_ = exec.Command("crontab", "-r").Run()
	} else {
		if err := writeCrontab(filtered + "\n"); err != nil {
			return err
		}
	}
	if profile != "" {
		fmt.Printf("%s%s%s Refresh cron removed for profile '%s'\n", ui.Green, ui.Check, ui.NC, profile)
	} else {
		fmt.Printf("%s%s%s All azprofile refresh crons removed\n", ui.Green, ui.Check, ui.NC)
	}
	return nil
}

func CronPIMRemove(profile string) error {
	tag := CronPIMTagPrefix
	if profile != "" {
		tag = cronPIMTag(profile)
	}
	filtered := filterOut(readCrontab(), tag)
	if filtered == "" {
		_ = exec.Command("crontab", "-r").Run()
	} else {
		if err := writeCrontab(filtered + "\n"); err != nil {
			return err
		}
	}
	if profile != "" {
		fmt.Printf("%s%s%s PIM cron removed for profile '%s'\n", ui.Green, ui.Check, ui.NC, profile)
	} else {
		fmt.Printf("%s%s%s All azprofile pim crons removed\n", ui.Green, ui.Check, ui.NC)
	}
	return nil
}

func CronStatus() {
	var refreshLines, pimLines []string
	for _, line := range strings.Split(readCrontab(), "\n") {
		if strings.Contains(line, CronTagPrefix) {
			refreshLines = append(refreshLines, line)
		} else if strings.Contains(line, CronPIMTagPrefix) {
			pimLines = append(pimLines, line)
		}
	}
	if len(refreshLines) == 0 && len(pimLines) == 0 {
		fmt.Printf("%s-%s No crons installed. Run: azprofile cron refresh install [profile]\n", ui.Dim, ui.NC)
		return
	}
	ablyConfigured := false
	if _, err := LoadConfig(); err == nil {
		ablyConfigured = true
	}
	if len(refreshLines) > 0 {
		ablyNote := ""
		if ablyConfigured {
			ablyNote = " (Ably auto-publish included)"
		}
		fmt.Printf("%s%s%s Refresh%s:\n", ui.Green, ui.Check, ui.NC, ablyNote)
		for _, l := range refreshLines {
			fmt.Printf("  %s%s%s\n", ui.Dim, l, ui.NC)
		}
	}
	if len(pimLines) > 0 {
		fmt.Printf("%s%s%s PIM:\n", ui.Green, ui.Check, ui.NC)
		for _, l := range pimLines {
			fmt.Printf("  %s%s%s\n", ui.Dim, l, ui.NC)
		}
	}
}
