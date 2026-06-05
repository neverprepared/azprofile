package azprofile

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/neverprepared/azprofile/internal/ui"
)

// Doctor checks that everything needed to run azprofile is present and wired up.
func Doctor() {
	allOk := true

	check := func(label string, pass bool, hint string) {
		if pass {
			fmt.Printf("%s%s%s %s\n", ui.Green, ui.Check, ui.NC, label)
		} else {
			fmt.Printf("%s%s%s %s%s%s\n", ui.Red, ui.Cross, ui.NC, ui.Bold, label, ui.NC)
			if hint != "" {
				fmt.Printf("     %s→ %s%s\n", ui.Dim, hint, ui.NC)
			}
			allOk = false
		}
	}
	warn := func(label, hint string) {
		fmt.Printf("%s!%s  %s%s%s\n", ui.Yellow, ui.NC, ui.Yellow, label, ui.NC)
		if hint != "" {
			fmt.Printf("     %s→ %s%s\n", ui.Dim, hint, ui.NC)
		}
	}

	fmt.Printf("%s%sSystem%s\n", ui.Bold, ui.Blue, ui.NC)
	_, azErr := exec.LookPath("az")
	check("az CLI installed", azErr == nil, "install Azure CLI: https://docs.microsoft.com/cli/azure/install-azure-cli")

	_, pimErr := exec.LookPath("az-pim-cli")
	if pimErr != nil {
		warn("az-pim-cli not found (PIM commands unavailable)", "install from: https://github.com/netr0m/az-pim-cli")
	} else {
		check("az-pim-cli installed", true, "")
	}

	fmt.Printf("\n%s%sProfiles%s\n", ui.Bold, ui.Blue, ui.NC)
	_, profDirErr := os.Stat(ProfilesDir())
	check("Profiles directory ("+ProfilesDir()+")", profDirErr == nil, "run: azprofile init <name>")

	current := GetCurrent()
	profileOk := current != "(none)" && current != "(unmigrated directory)"
	if profileOk {
		check("Active profile ("+current+")", true, "")
	} else {
		check("Active profile — none set", false, "run: azprofile use <name>")
	}

	fmt.Printf("\n%s%sEncryption & Sync%s\n", ui.Bold, ui.Blue, ui.NC)
	k, keyErr := LoadMasterKey()
	if keyErr != nil {
		check("Master encryption key", false, "run: azprofile setup (new machine) or azprofile sync join (receiver)")
	} else {
		check("Master encryption key (fingerprint: "+KeyFingerprint(k)+")", true, "")
		if v := os.Getenv(envMasterKey); v != "" {
			warn("AZPROFILE_MASTER_KEY env var is set — overrides keychain key", "unset it if unintended")
		}
	}

	cfg, cfgErr := LoadConfig()
	if cfgErr != nil {
		check("Ably config", false, "run: azprofile setup")
	} else {
		ablyDisplay := cfg.AblyAPIKey
		if len(ablyDisplay) > 8 {
			ablyDisplay = ablyDisplay[:8] + "…"
		}
		check("Ably config (prefix: "+cfg.ChannelPrefix+", key: "+ablyDisplay+")", true, "")
	}

	fmt.Printf("\n%s%sCron%s\n", ui.Bold, ui.Blue, ui.NC)
	crontab := readCrontab()
	hasRefresh := strings.Contains(crontab, CronTagPrefix)
	hasPIM := strings.Contains(crontab, CronPIMTagPrefix)

	ablyNote := ""
	if cfg != nil {
		ablyNote = " (includes Ably auto-publish)"
	}
	if hasRefresh {
		check("Refresh cron"+ablyNote, true, "")
	} else {
		warn("No refresh cron — tokens will expire without manual refresh", "run: azprofile cron refresh install")
	}
	if hasPIM {
		check("PIM cron", true, "")
	}

	fmt.Println()
	if allOk {
		fmt.Printf("%s%s%s All checks passed.%s\n", ui.Green, ui.Bold, ui.Check, ui.NC)
	} else {
		fmt.Printf("%s%s%s Some checks failed — see hints above.%s\n", ui.Yellow, ui.Bold, ui.Arrow, ui.NC)
	}
}
