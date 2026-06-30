package azprofile

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/neverprepared/azprofile/internal/ui"
	"golang.org/x/term"
)

func GetCurrent() string {
	link := ActiveLink()
	fi, err := os.Lstat(link)
	if err != nil {
		return "(none)"
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(link)
		if err != nil {
			return "(none)"
		}
		return filepath.Base(target)
	}
	if fi.IsDir() {
		return "(unmigrated directory)"
	}
	return "(none)"
}

func MigrateIfNeeded() error {
	link := ActiveLink()
	fi, err := os.Lstat(link)
	if err != nil {
		return nil
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return nil
	}
	if err := EnsureProfilesDir(); err != nil {
		return err
	}
	target := ProfilePath("default")
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("Cannot migrate: %s already exists", target)
	}
	fmt.Printf("%s%s%s Migrating existing .azure/ to profile 'default'...\n", ui.Yellow, ui.Arrow, ui.NC)
	if err := os.Rename(link, target); err != nil {
		return err
	}
	if err := os.Symlink(target, link); err != nil {
		return err
	}
	fmt.Printf("%s%s%s Migrated. Active profile is now 'default'.\n\n", ui.Green, ui.Check, ui.NC)
	return nil
}

func Current() {
	fmt.Printf("%s%sActive profile:%s %s\n", ui.Bold, ui.Blue, ui.NC, GetCurrent())
}

func List() error {
	if err := EnsureProfilesDir(); err != nil {
		return err
	}
	current := GetCurrent()

	fmt.Printf("%s%sAzure Profiles%s\n", ui.Bold, ui.Blue, ui.NC)
	fmt.Printf("%s──────────────%s\n", ui.Dim, ui.NC)

	entries, err := os.ReadDir(ProfilesDir())
	if err != nil {
		return err
	}
	found := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		found++
		name := e.Name()
		if name == current {
			fmt.Printf("  %s%s%s %s%s%s %s(active)%s\n",
				ui.Green, ui.Check, ui.NC, ui.Bold, name, ui.NC, ui.Dim, ui.NC)
		} else {
			fmt.Printf("  %s-%s %s\n", ui.Dim, ui.NC, name)
		}
	}
	if found == 0 {
		fmt.Printf("  %sNo profiles. Run: azprofile init <name>%s\n", ui.Dim, ui.NC)
	}
	return nil
}

func Use(name string) error {
	if name == "" {
		return fmt.Errorf("Usage: azprofile use <name>")
	}
	if err := ValidateProfileName(name); err != nil {
		return err
	}
	if err := MigrateIfNeeded(); err != nil {
		return err
	}
	target := ProfilePath(name)
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		return fmt.Errorf("Profile '%s' not found. Run: azprofile init %s", name, name)
	}
	link := ActiveLink()
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(link); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("%s exists and is not a symlink. Run azprofile again to migrate.", link)
		}
	}
	if err := os.Symlink(target, link); err != nil {
		return err
	}
	fmt.Printf("%s%s%s Switched to %s%s%s\n", ui.Green, ui.Check, ui.NC, ui.Bold, name, ui.NC)

	if _, err := exec.LookPath("az"); err == nil {
		cmd := exec.Command("az", "account", "show",
			"--query", "{user:user.name, subscription:name, tenant:tenantId}",
			"-o", "tsv")
		cmd.Env = append(os.Environ(), "AZURE_CONFIG_DIR="+target)
		out, err := cmd.Output()
		if err == nil {
			line := strings.TrimSpace(string(out))
			if line != "" {
				if fields := strings.Fields(line); len(fields) > 0 {
					fmt.Printf("%s  %s%s\n", ui.Dim, fields[0], ui.NC)
				}
			}
		}
	}
	return nil
}

// SetupWizard interactively configures the master encryption key and Ably sync.
// Steps that are already configured are skipped. Returns without prompting if
// stdin is not a TTY.
func SetupWizard() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}

	scanner := bufio.NewScanner(os.Stdin)
	readLine := func(prompt string) string {
		fmt.Print(prompt)
		if !scanner.Scan() {
			return ""
		}
		return strings.TrimSpace(scanner.Text())
	}
	readSecret := func(prompt string) (string, error) {
		fmt.Print(prompt)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}

	// ── Step 1: master encryption key ────────────────────────────────────────
	fmt.Printf("\n%s%sStep 1/2 — Encryption key%s\n", ui.Bold, ui.Blue, ui.NC)

	k, keyErr := LoadMasterKey()
	if keyErr == nil {
		fmt.Printf("%s%s%s Master key already configured %s(fingerprint: %s)%s\n",
			ui.Green, ui.Check, ui.NC, ui.Dim, KeyFingerprint(k), ui.NC)
	} else {
		fmt.Printf("%s%s%s No master key found.%s\n", ui.Yellow, ui.Arrow, ui.NC, ui.NC)
		fmt.Printf("%s  [g] Generate a new key   [i] Import an existing hex key%s\n", ui.Dim, ui.NC)
		choice := readLine("  Choice [g]: ")
		if choice == "" {
			choice = "g"
		}
		switch strings.ToLower(choice) {
		case "g", "generate":
			k, err := NewMasterKey()
			if err != nil {
				return fmt.Errorf("keygen: %w", err)
			}
			if err := SaveMasterKey(k); err != nil {
				return fmt.Errorf("save key: %w", err)
			}
			fmt.Printf("%s%s%s Generated and saved. Fingerprint: %s%s%s\n",
				ui.Green, ui.Check, ui.NC, ui.Dim, KeyFingerprint(k), ui.NC)
			fmt.Printf("%s  Export the hex key to other machines with: azprofile sync export-key --confirm%s\n", ui.Dim, ui.NC)
		case "i", "import":
			hexKey, err := readSecret("  Paste hex key: ")
			if err != nil {
				return fmt.Errorf("read key: %w", err)
			}
			k, err := KeyFromHex(hexKey)
			if err != nil {
				return fmt.Errorf("invalid key: %w", err)
			}
			if err := SaveMasterKey(k); err != nil {
				return fmt.Errorf("save key: %w", err)
			}
			fmt.Printf("%s%s%s Key imported. Fingerprint: %s%s%s\n",
				ui.Green, ui.Check, ui.NC, ui.Dim, KeyFingerprint(k), ui.NC)
		default:
			return fmt.Errorf("unknown choice %q — expected 'g' or 'i'", choice)
		}
	}

	// ── Step 2: Ably sync config ──────────────────────────────────────────────
	fmt.Printf("\n%s%sStep 2/2 — Ably sync%s\n", ui.Bold, ui.Blue, ui.NC)

	_, cfgErr := LoadConfig()
	if cfgErr == nil {
		fmt.Printf("%s%s%s Ably already configured.%s\n", ui.Green, ui.Check, ui.NC, ui.NC)
	} else {
		fmt.Printf("%s%s%s No Ably config found.%s\n", ui.Yellow, ui.Arrow, ui.NC, ui.NC)
		fmt.Printf("%s  Leave blank to skip Ably setup (you can run 'azprofile sync configure' later).%s\n", ui.Dim, ui.NC)

		ablyKey, err := readSecret("  Ably API key: ")
		if err != nil {
			return fmt.Errorf("read ably key: %w", err)
		}
		if ablyKey == "" {
			fmt.Printf("%s%s%s Skipped — Ably not configured.%s\n", ui.Yellow, ui.Arrow, ui.NC, ui.NC)
		} else {
			prefix := readLine("  Channel prefix [azprofile]: ")
			if prefix == "" {
				prefix = "azprofile"
			}
			cur, _ := LoadConfig()
			cfg := &SyncConfig{
				AblyAPIKey:    ablyKey,
				ChannelPrefix: prefix,
			}
			if cur != nil {
				cfg.SenderID = cur.SenderID
			}
			if err := SaveConfig(cfg); err != nil {
				return fmt.Errorf("save ably config: %w", err)
			}
			fmt.Printf("%s%s%s Ably configured (prefix: %s).%s\n",
				ui.Green, ui.Check, ui.NC, prefix, ui.NC)
		}
	}

	return nil
}

func Init(name string, login bool, opts LoginOptions) error {
	if name == "" {
		return fmt.Errorf("Usage: azprofile init <name>")
	}
	if err := ValidateProfileName(name); err != nil {
		return err
	}
	if err := MigrateIfNeeded(); err != nil {
		return err
	}
	if err := EnsureProfilesDir(); err != nil {
		return err
	}
	target := ProfilePath(name)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}

	fmt.Printf("%s%sInitializing profile '%s'%s\n", ui.Bold, ui.Blue, name, ui.NC)
	fmt.Printf("%s%s%s Config dir: %s%s%s\n", ui.Cyan, ui.Arrow, ui.NC, ui.Dim, target, ui.NC)

	fmt.Printf("\n%s%s%s Profile '%s' initialized.\n", ui.Green, ui.Check, ui.NC, name)
	if login {
		fmt.Println()
		if err := runAzLogin(target, opts); err != nil {
			return err
		}
	} else {
		fmt.Printf("%s  Login with: azprofile login %s%s\n", ui.Dim, name, ui.NC)
	}
	fmt.Printf("%s  Switch to it with: azprofile use %s%s\n", ui.Dim, name, ui.NC)

	if _, err := LoadMasterKey(); err != nil {
		fmt.Printf("%s  First time? Run: azprofile setup%s\n", ui.Dim, ui.NC)
	}

	PublishIfConfigured(name)
	return nil
}

// Delete removes a profile directory. If the profile is currently active the
// symlink is removed too. Refuses if it's the only remaining profile.
func Delete(name string) error {
	if name == "" {
		return fmt.Errorf("Usage: azprofile delete <name>")
	}
	if err := ValidateProfileName(name); err != nil {
		return err
	}
	target := ProfilePath(name)
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		return fmt.Errorf("profile '%s' not found", name)
	}

	// refuse to delete the only profile
	entries, err := os.ReadDir(ProfilesDir())
	if err != nil {
		return err
	}
	dirs := 0
	for _, e := range entries {
		if e.IsDir() {
			dirs++
		}
	}
	if dirs <= 1 {
		return fmt.Errorf("cannot delete the only profile — create another first")
	}

	isActive := GetCurrent() == name
	if isActive {
		link := ActiveLink()
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("remove active symlink: %w", err)
		}
		fmt.Printf("%s%s%s Removed active symlink (no profile active)\n", ui.Yellow, ui.Arrow, ui.NC)
	}

	if err := os.RemoveAll(target); err != nil {
		return err
	}
	fmt.Printf("%s%s%s Deleted profile '%s'\n", ui.Green, ui.Check, ui.NC, name)
	if isActive {
		fmt.Printf("%s  Switch to another profile with: azprofile use <name>%s\n", ui.Dim, ui.NC)
	}
	return nil
}

// JoinWizard is the receiver-side onboarding: import a master key from hex and
// configure Ably sync. Does not generate a new key — get the hex from the
// sender with: azprofile sync export-key --confirm
func JoinWizard() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("join wizard requires an interactive terminal")
	}

	scanner := bufio.NewScanner(os.Stdin)
	readLine := func(prompt string) string {
		fmt.Print(prompt)
		if !scanner.Scan() {
			return ""
		}
		return strings.TrimSpace(scanner.Text())
	}
	readSecret := func(prompt string) (string, error) {
		fmt.Print(prompt)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}

	fmt.Printf("%s%sReceiver setup — import encryption key and configure Ably%s\n", ui.Bold, ui.Blue, ui.NC)
	fmt.Printf("%s  Get the hex key from the sender: azprofile sync export-key --confirm%s\n\n", ui.Dim, ui.NC)

	hexKey, err := readSecret("Encryption key (hex): ")
	if err != nil {
		return fmt.Errorf("read key: %w", err)
	}
	if hexKey == "" {
		return fmt.Errorf("encryption key is required")
	}
	k, err := KeyFromHex(hexKey)
	if err != nil {
		return fmt.Errorf("invalid key: %w", err)
	}
	if err := SaveMasterKey(k); err != nil {
		return fmt.Errorf("save key: %w", err)
	}
	fmt.Printf("%s%s%s Key imported. Fingerprint: %s%s%s\n",
		ui.Green, ui.Check, ui.NC, ui.Dim, KeyFingerprint(k), ui.NC)

	ablyKey, err := readSecret("Ably API key: ")
	if err != nil {
		return fmt.Errorf("read ably key: %w", err)
	}
	if ablyKey == "" {
		return fmt.Errorf("Ably API key is required")
	}
	prefix := readLine("Channel prefix [azprofile]: ")
	if prefix == "" {
		prefix = "azprofile"
	}
	cur, _ := LoadConfig()
	cfg := &SyncConfig{AblyAPIKey: ablyKey, ChannelPrefix: prefix}
	if cur != nil {
		cfg.SenderID = cur.SenderID
	}
	if err := SaveConfig(cfg); err != nil {
		return fmt.Errorf("save ably config: %w", err)
	}
	fmt.Printf("%s%s%s Ably configured (prefix: %s).%s\n",
		ui.Green, ui.Check, ui.NC, prefix, ui.NC)
	fmt.Printf("\n%s%s%s Ready. Run: azprofile sync subscribe%s\n", ui.Green, ui.Check, ui.NC, ui.NC)
	return nil
}

func Login(name string, opts LoginOptions) error {
	if name == "" {
		name = GetCurrent()
		if name == "(none)" || name == "(unmigrated directory)" {
			return fmt.Errorf("No active profile. Specify one: azprofile login <name>")
		}
	}
	if err := ValidateProfileName(name); err != nil {
		return err
	}
	target := ProfilePath(name)
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		return fmt.Errorf("Profile '%s' not found. Run: azprofile init %s", name, name)
	}

	fmt.Printf("%s%sRe-authenticating profile '%s'%s\n", ui.Bold, ui.Blue, name, ui.NC)
	fmt.Printf("%s%s%s Config dir: %s%s%s\n\n", ui.Cyan, ui.Arrow, ui.NC, ui.Dim, target, ui.NC)

	if err := runAzLogin(target, opts); err != nil {
		return err
	}

	fmt.Printf("\n%s%s%s Profile '%s' re-authenticated.\n", ui.Green, ui.Check, ui.NC, name)
	PublishIfConfigured(name)
	return nil
}

// LoginOptions are passthrough flags forwarded to `az login`. Zero value
// (empty fields) reproduces a plain `az login`.
type LoginOptions struct {
	Tenant string // --tenant
	Scope  string // --scope, e.g. https://graph.microsoft.com/.default
}

func runAzLogin(configDir string, opts LoginOptions) error {
	args := []string{"login"}
	if opts.Tenant != "" {
		args = append(args, "--tenant", opts.Tenant)
	}
	if opts.Scope != "" {
		args = append(args, "--scope", opts.Scope)
	}
	cmd := exec.Command("az", args...)
	cmd.Env = append(os.Environ(), "AZURE_CONFIG_DIR="+configDir)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func Whoami() error {
	if _, err := exec.LookPath("az"); err != nil {
		return fmt.Errorf("az CLI not found")
	}
	current := GetCurrent()
	fmt.Printf("%s%sProfile:%s %s\n", ui.Bold, ui.Blue, ui.NC, current)
	fmt.Printf("%s──────────────%s\n", ui.Dim, ui.NC)
	cmd := exec.Command("az", "account", "show", "-o", "table")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
