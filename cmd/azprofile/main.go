package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/neverprepared/azprofile/internal/azprofile"
	"github.com/neverprepared/azprofile/internal/ui"
)

func main() {
	root := &cobra.Command{
		Use:     "azprofile",
		Short:   "Azure multi-identity manager",
		Long:    "azprofile — Azure multi-identity manager. Create, switch, refresh, and sync Azure CLI identities.",
		Version: azprofile.Version,
	}
	root.SilenceUsage = true
	root.SilenceErrors = true

	root.AddCommand(
		listCmd(),
		useCmd(),
		currentCmd(),
		initCmd(),
		deleteCmd(),
		loginCmd(),
		whoamiCmd(),
		setupCmd(),
		cronCmd(),
		refreshCmd(),
		syncCmd(),
		pimCmd(),
		doctorCmd(),
		updateCmd(),
	)

	if err := root.Execute(); err != nil {
		ui.Die("%s", err.Error())
	}
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show all profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			return azprofile.List()
		},
	}
}

func useCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Switch to a profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return azprofile.Use(args[0])
		},
	}
}

func currentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show active profile name (scriptable)",
		RunE: func(cmd *cobra.Command, args []string) error {
			azprofile.Current()
			return nil
		},
	}
}

func initCmd() *cobra.Command {
	var login bool
	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: "Create a new profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return azprofile.Init(args[0], login)
		},
	}
	cmd.Flags().BoolVar(&login, "login", false, "Run az login after creating the profile")
	return cmd
}

func deleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return azprofile.Delete(args[0])
		},
	}
}

func loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login [name]",
		Short: "Re-authenticate a profile (default: active)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return azprofile.Login(name)
		},
	}
}

func whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the active identity (profile + Azure account)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return azprofile.Whoami()
		},
	}
}

// setupCmd runs the global, idempotent wizard for encryption key + Ably config.
// Operates on machine-wide state (keychain + config.enc), not per-profile.
func setupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "One-time wizard: configure encryption key and Ably sync",
		Long: `Interactive wizard for global sync setup. Run once per machine.

Sender (first machine):
  azprofile setup          # generates key, sets Ably config
  azprofile sync export-key --confirm   # copy hex to receiver

Receiver (other machines):
  azprofile sync join      # import key + Ably config in one shot`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return azprofile.SetupWizard()
		},
	}
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that all dependencies and configuration are in place",
		RunE: func(cmd *cobra.Command, args []string) error {
			azprofile.Doctor()
			return nil
		},
	}
}

func refreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh [profiles...]",
		Short: "Refresh Azure tokens (all profiles or specific ones)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if failures := azprofile.Refresh(args); failures > 0 {
				return fmt.Errorf("refresh failed for %d profile(s)", failures)
			}
			return nil
		},
	}
}

func updateCmd() *cobra.Command {
	var (
		check bool
		yes   bool
		force bool
	)
	c := &cobra.Command{
		Use:   "update",
		Short: "Check for a new release and (with confirmation) replace this binary",
		Long:  "Queries the GitHub Releases API for the latest azprofile tag, compares it against the embedded version, and on confirmation downloads, SHA256-verifies, and atomically replaces the running binary. The release tarball is fetched over HTTPS and verified against the matching `*-checksums.txt` asset in the same release.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return azprofile.Update(azprofile.UpdateOptions{Check: check, Yes: yes, Force: force})
		},
	}
	c.Flags().BoolVar(&check, "check", false, "Only check for an update; don't download or replace")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt")
	c.Flags().BoolVar(&force, "force", false, "Allow updating a dev build or reinstalling the same/older version")
	return c
}

// ── cron ─────────────────────────────────────────────────────────────────────

func cronCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "cron",
		Short: "Manage scheduled automation (token refresh, PIM activation)",
	}
	c.AddCommand(cronRefreshCmd(), cronPIMCmd(), cronStatusCmd())
	return c
}

func cronStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show installed cron entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			azprofile.CronStatus()
			return nil
		},
	}
}

func cronRefreshCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "refresh",
		Short: "Manage token-refresh cron entries",
		Long:  "Install or remove the hourly cron that refreshes Azure tokens. When Ably sync is configured the refresh cron also auto-publishes the updated profile.",
	}

	install := &cobra.Command{
		Use:   "install [profile] [schedule]",
		Short: "Install a refresh cron (all profiles or one)",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, schedule := "", ""
			if len(args) >= 1 {
				profile = args[0]
			}
			if len(args) >= 2 {
				schedule = args[1]
			}
			return azprofile.CronInstall(profile, schedule)
		},
	}
	remove := &cobra.Command{
		Use:   "remove [profile]",
		Short: "Remove refresh cron (specific profile, or all if omitted)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := ""
			if len(args) == 1 {
				profile = args[0]
			}
			return azprofile.CronRemove(profile)
		},
	}
	c.AddCommand(install, remove)
	return c
}

func cronPIMCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "pim",
		Short: "Manage daily PIM role activation crons",
	}

	install := &cobra.Command{
		Use:   "install <profile> <role>... | <profile> --all",
		Short: "Install a cron that activates PIM roles for a profile",
		Long:  "Installs a daily cron that refreshes tokens and activates PIM role assignments. Pass role names as positional args, or --all to activate every eligibility (filtered by --type / --role).",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := args[0]
			roles := args[1:]
			opts := azprofile.PIMCronOpts{}
			opts.All, _ = cmd.Flags().GetBool("all")
			opts.Type, _ = cmd.Flags().GetString("type")
			opts.Role, _ = cmd.Flags().GetString("role")
			opts.Duration, _ = cmd.Flags().GetInt("duration")
			opts.Reason, _ = cmd.Flags().GetString("reason")
			opts.TicketSystem, _ = cmd.Flags().GetString("ticket-system")
			opts.TicketNumber, _ = cmd.Flags().GetString("ticket-number")
			schedule, _ := cmd.Flags().GetString("schedule")
			return azprofile.CronPIMInstall(profile, schedule, roles, opts)
		},
	}
	install.Flags().String("schedule", "", "Cron schedule (default \"30 8 * * *\")")
	addPimActivateFlags(install)

	remove := &cobra.Command{
		Use:   "remove [profile]",
		Short: "Remove PIM cron (specific profile, or all if omitted)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := ""
			if len(args) == 1 {
				profile = args[0]
			}
			return azprofile.CronPIMRemove(profile)
		},
	}

	c.AddCommand(install, remove)
	return c
}

// ── pim ──────────────────────────────────────────────────────────────────────

// addPimActivateFlags registers the shared PIM activation flags onto cmd.
// Used by both `pim activate` and `cron pim install` to keep them in sync.
func addPimActivateFlags(cmd *cobra.Command) {
	cmd.Flags().BoolP("all", "", false, "Activate every eligible assignment (filtered by --type / --role)")
	cmd.Flags().StringP("type", "t", "all", "Restrict to: all | resource | role | group")
	cmd.Flags().StringP("role", "r", "", "Role name filter / disambiguator")
	cmd.Flags().IntP("duration", "d", azprofile.DefaultPIMDuration, "Activation duration in minutes")
	cmd.Flags().String("reason", azprofile.DefaultPIMReason, "Reason for activation")
	cmd.Flags().String("ticket-system", "", "Ticket system name")
	cmd.Flags().String("ticket-number", "", "Ticket number")
}

func pimCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "pim",
		Short: "Privileged Identity Management role activation",
		Long:  "Native PIM client. Talks directly to ARM and the RBAC PIM API; uses `az account get-access-token` for auth, so the active azprofile identity is the principal.",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "Show eligible role assignments",
		RunE: func(cmd *cobra.Command, args []string) error {
			typeFlag, _ := cmd.Flags().GetString("type")
			return azprofile.PimList(typeFlag)
		},
	}
	list.Flags().StringP("type", "t", "all", "Filter: all | resource | role | group")

	active := &cobra.Command{
		Use:   "active",
		Short: "Show currently active roles",
		RunE: func(cmd *cobra.Command, args []string) error {
			typeFlag, _ := cmd.Flags().GetString("type")
			return azprofile.PimActive(typeFlag)
		},
	}
	active.Flags().StringP("type", "t", "all", "Filter: all | resource | role | group")

	activate := &cobra.Command{
		Use:   "activate <name> [name...] | --all",
		Short: "Activate one or more eligible role assignments",
		Long:  "Looks up <name> across resource, role, and group eligibility. Errors if the name is ambiguous. Pass --all to activate every eligibility (filtered by --type / --role).",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := azprofile.ActivateOptions{}
			opts.All, _ = cmd.Flags().GetBool("all")
			opts.Type, _ = cmd.Flags().GetString("type")
			opts.Role, _ = cmd.Flags().GetString("role")
			opts.DurationMin, _ = cmd.Flags().GetInt("duration")
			opts.Reason, _ = cmd.Flags().GetString("reason")
			opts.StartDate, _ = cmd.Flags().GetString("start-date")
			opts.StartTime, _ = cmd.Flags().GetString("start-time")
			opts.TicketSystem, _ = cmd.Flags().GetString("ticket-system")
			opts.TicketNumber, _ = cmd.Flags().GetString("ticket-number")
			opts.Wait, _ = cmd.Flags().GetBool("wait")
			opts.WaitTimeout, _ = cmd.Flags().GetInt("timeout")
			opts.Yes, _ = cmd.Flags().GetBool("yes")
			return azprofile.PimActivate(args, opts)
		},
	}
	addPimActivateFlags(activate)
	activate.Flags().String("start-date", "", "Start date (DD/MM/YYYY); defaults to now")
	activate.Flags().String("start-time", "", "Start time (HH:MM); defaults to now")
	activate.Flags().Bool("wait", true, "Wait for activation to complete")
	activate.Flags().Int("timeout", 300, "Wait timeout in seconds")
	activate.Flags().BoolP("yes", "y", false, "Skip confirmation prompt in --all mode")

	deactivate := &cobra.Command{
		Use:   "deactivate <name> [name...]",
		Short: "Deactivate one or more active role assignments",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := azprofile.DeactivateOptions{}
			opts.Type, _ = cmd.Flags().GetString("type")
			opts.Role, _ = cmd.Flags().GetString("role")
			return azprofile.PimDeactivate(args, opts)
		},
	}
	deactivate.Flags().StringP("type", "t", "all", "Restrict lookup: all | resource | role | group")
	deactivate.Flags().StringP("role", "r", "", "Role to deactivate when multiple exist for the same name")

	c.AddCommand(list, active, activate, deactivate)
	return c
}

// ── sync ─────────────────────────────────────────────────────────────────────

func syncCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "sync",
		Short: "Sync profiles between machines via rsync or Ably pub/sub",
		Long: `Two transport modes:

  rsync   push/pull a profile directory to/from a local path (USB, NAS, etc.)
  ably    publish/receive an encrypted credential bundle over Ably pub/sub

Sender setup (first machine):
  azprofile setup                        # generate key + configure Ably
  azprofile cron refresh install         # auto-publish on every token refresh
  azprofile sync export-key --confirm    # copy hex key to receiver

Receiver setup (other machines):
  azprofile sync join                    # import key + Ably config in one shot
  azprofile sync subscribe               # start listening for updates

Environment:
  AZPROFILE_HOME           Base directory (default: $HOME)
  AZPROFILE_SYNC           Default rsync directory (used if <dir> is omitted)
  AZPROFILE_MASTER_KEY     Hex-encoded master key (overrides OS keychain)`,
	}

	// ── rsync push / pull ────────────────────────────────────────────────────
	push := &cobra.Command{
		Use:   "push [dir] [profile]",
		Short: "Push a profile to a local directory via rsync",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 2 {
				return fmt.Errorf("too many arguments")
			}
			return runRsyncSync("push", args)
		},
	}

	pull := &cobra.Command{
		Use:   "pull [dir] [profile]",
		Short: "Pull a profile from a local directory via rsync",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 2 {
				return fmt.Errorf("too many arguments")
			}
			return runRsyncSync("pull", args)
		},
	}

	// ── Ably publish / receive / subscribe ───────────────────────────────────
	publish := &cobra.Command{
		Use:   "publish [profile]",
		Short: "Encrypt and publish the active profile to Ably (one-shot)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := ""
			if len(args) == 1 {
				profile = args[0]
			}
			return azprofile.PublishProfile(cmd.Context(), profile)
		},
	}

	receive := &cobra.Command{
		Use:   "receive [profile]",
		Short: "Pull the latest update from Ably history (one-shot)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := ""
			if len(args) == 1 {
				profile = args[0]
			}
			return azprofile.PullOnce(cmd.Context(), profile)
		},
	}

	subscribe := &cobra.Command{
		Use:   "subscribe [profile]",
		Short: "Subscribe to Ably and apply updates as they arrive (daemon)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := ""
			if len(args) == 1 {
				profile = args[0]
			}
			return azprofile.Subscribe(cmd.Context(), profile)
		},
	}

	// ── join (receiver onboarding) ───────────────────────────────────────────
	join := &cobra.Command{
		Use:   "join",
		Short: "Receiver setup wizard: import key and configure Ably",
		Long: `Import the sender's encryption key (hex) and configure the Ably channel.
Get the hex key from the sender with: azprofile sync export-key --confirm`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return azprofile.JoinWizard()
		},
	}

	// ── key management ───────────────────────────────────────────────────────
	keygen := &cobra.Command{
		Use:   "keygen",
		Short: "Generate a new master encryption key and store it in the OS keychain",
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")
			if !force {
				if _, err := azprofile.LoadMasterKey(); err == nil {
					return fmt.Errorf("master key already exists; pass --force to overwrite")
				}
			}
			k, err := azprofile.NewMasterKey()
			if err != nil {
				return err
			}
			if err := azprofile.SaveMasterKey(k); err != nil {
				return err
			}
			fmt.Printf("%s%s%s Key generated. Fingerprint: %s%s%s\n",
				ui.Green, ui.Check, ui.NC, ui.Dim, azprofile.KeyFingerprint(k), ui.NC)
			fmt.Fprintln(os.Stderr, "To transfer to another machine: azprofile sync export-key --confirm")
			return nil
		},
	}
	keygen.Flags().Bool("force", false, "Overwrite an existing key")

	importKey := &cobra.Command{
		Use:   "import-key <hex>",
		Short: "Import a master key from hex into the OS keychain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := azprofile.KeyFromHex(args[0])
			if err != nil {
				return err
			}
			if err := azprofile.SaveMasterKey(k); err != nil {
				return err
			}
			fmt.Printf("%s%s%s Key imported. Fingerprint: %s%s%s\n",
				ui.Green, ui.Check, ui.NC, ui.Dim, azprofile.KeyFingerprint(k), ui.NC)
			return nil
		},
	}

	exportKey := &cobra.Command{
		Use:   "export-key",
		Short: "Print the master key as hex (requires --confirm)",
		RunE: func(cmd *cobra.Command, args []string) error {
			confirm, _ := cmd.Flags().GetBool("confirm")
			if !confirm {
				return fmt.Errorf("refusing to print master key without --confirm")
			}
			k, err := azprofile.LoadMasterKey()
			if err != nil {
				return err
			}
			fmt.Println(azprofile.KeyToHex(k))
			return nil
		},
	}
	exportKey.Flags().Bool("confirm", false, "Acknowledge that the key will be printed to stdout")

	configure := &cobra.Command{
		Use:   "configure",
		Short: "Write Ably sync config non-interactively (for automation/key rotation)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ablyKey, _ := cmd.Flags().GetString("ably-key")
			prefix, _ := cmd.Flags().GetString("channel-prefix")
			if ablyKey == "" {
				return fmt.Errorf("--ably-key is required")
			}
			cur, _ := azprofile.LoadConfig()
			cfg := &azprofile.SyncConfig{
				AblyAPIKey:    ablyKey,
				ChannelPrefix: prefix,
			}
			if cur != nil {
				cfg.SenderID = cur.SenderID
			}
			return azprofile.SaveConfig(cfg)
		},
	}
	configure.Flags().String("ably-key", "", "Ably API key (form: appId.keyId:secret)")
	configure.Flags().String("channel-prefix", "azprofile", "Channel prefix")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show sync configuration and last publish/receive times",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncStatus()
		},
	}

	c.AddCommand(push, pull, publish, receive, subscribe, join,
		keygen, importKey, exportKey, configure, status)
	return c
}

func runRsyncSync(action string, args []string) error {
	dir, profile := "", ""
	if len(args) >= 1 {
		dir = args[0]
	}
	if len(args) >= 2 {
		profile = args[1]
	}
	return azprofile.Sync(action, dir, profile)
}

func runSyncStatus() error {
	fmt.Printf("Config path:    %s\n", azprofile.ConfigPath())
	fmt.Printf("State path:     %s\n", azprofile.StatePath())
	fmt.Printf("Master key:     %s\n", azprofile.MasterKeySource())

	if v := os.Getenv("AZPROFILE_MASTER_KEY"); v != "" {
		fmt.Printf("%s  ⚠ AZPROFILE_MASTER_KEY env var is set and overrides the keychain key%s\n", ui.Yellow, ui.NC)
	}

	key, err := azprofile.LoadMasterKey()
	if err != nil {
		fmt.Printf("Status:         not configured (%s)\n", err.Error())
		return nil
	}
	fmt.Printf("Key fingerprint: %s\n", azprofile.KeyFingerprint(key))

	cfg, err := azprofile.LoadConfig()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("Config:         not written yet — run: azprofile setup")
			return nil
		}
		return err
	}
	fmt.Printf("Channel prefix: %s\n", cfg.ChannelPrefix)
	fmt.Printf("Sender ID:      %s\n", cfg.SenderID)
	if len(cfg.AblyAPIKey) > 8 {
		fmt.Printf("Ably API key:   %s…(%d chars)\n", cfg.AblyAPIKey[:8], len(cfg.AblyAPIKey))
	}

	state, err := azprofile.LoadState()
	if err == nil && (len(state.LastPublish) > 0 || len(state.LastReceive) > 0) {
		fmt.Println()
		for profile, t := range state.LastPublish {
			fmt.Printf("Last published: %-20s %s\n", profile, t.Local().Format(time.RFC3339))
		}
		for profile, t := range state.LastReceive {
			fmt.Printf("Last received:  %-20s %s\n", profile, t.Local().Format(time.RFC3339))
		}
	}
	return nil
}
