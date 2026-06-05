package azprofile

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ably/ably-go/ably"

	"github.com/neverprepared/azprofile/internal/ui"
)

// resolveProfileForSync picks the profile to sync. Falls back to the active
// profile (via the .azure symlink) if name is empty.
func resolveProfileForSync(name string) (string, string, error) {
	if name == "" {
		name = GetCurrent()
		if name == "(none)" || name == "(unmigrated directory)" {
			return "", "", errors.New("no profile specified and no active profile")
		}
	}
	dir := ProfilePath(name)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", "", fmt.Errorf("profile '%s' not found at %s", name, dir)
	}
	return name, dir, nil
}

// PublishProfile encrypts and publishes the four sync files for `profile`
// to Ably. Configuration must already be present (config.enc + master key).
func PublishProfile(ctx context.Context, profile string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	key, err := LoadMasterKey()
	if err != nil {
		return err
	}
	name, dir, err := resolveProfileForSync(profile)
	if err != nil {
		return err
	}
	upn, err := AzureUPNFromProfile(dir)
	if err != nil {
		return err
	}
	files, err := CollectFiles(dir)
	if err != nil {
		return err
	}
	seq, err := NextSeq(name)
	if err != nil {
		return err
	}
	env := BuildEnvelope(cfg.SenderID, name, AzureUPNHash(upn), seq, files)
	pt, err := env.Marshal()
	if err != nil {
		return err
	}
	ct, err := EncryptGCM(key, pt)
	if err != nil {
		return err
	}
	payload := base64.StdEncoding.EncodeToString(ct)

	channel := ChannelName(cfg.ChannelPrefix, upn, name)

	client, err := ably.NewREST(ably.WithKey(cfg.AblyAPIKey))
	if err != nil {
		return err
	}
	ch := client.Channels.Get(channel)
	if err := ch.Publish(ctx, ablyMessageName, payload); err != nil {
		return fmt.Errorf("ably publish: %w", err)
	}
	RecordPublish(name)
	fmt.Printf("%s%s%s Published %s%s%s (seq %d) → %s%s%s\n",
		ui.Green, ui.Check, ui.NC,
		ui.Bold, name, ui.NC, seq,
		ui.Dim, channel, ui.NC)
	return nil
}

// PublishIfConfigured is the best-effort hook called by refresh/init/login.
// Silently no-ops if config is missing; logs a one-line warning otherwise.
func PublishIfConfigured(profile string) {
	if _, err := os.Stat(ConfigPath()); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := PublishProfile(ctx, profile); err != nil {
		fmt.Printf("  %s%s%s ably auto-publish failed: %s%s%s\n",
			ui.Yellow, ui.Cross, ui.NC, ui.Dim, err.Error(), ui.NC)
	}
}

// PullOnce reads the latest persisted message from Ably history and applies it.
func PullOnce(ctx context.Context, profile string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	key, err := LoadMasterKey()
	if err != nil {
		return err
	}
	name, dir, err := resolveProfileForSync(profile)
	if err != nil {
		return err
	}
	upn, err := AzureUPNFromProfile(dir)
	if err != nil {
		return err
	}
	channel := ChannelName(cfg.ChannelPrefix, upn, name)

	client, err := ably.NewREST(ably.WithKey(cfg.AblyAPIKey))
	if err != nil {
		return err
	}
	ch := client.Channels.Get(channel)
	items, err := ch.History(ably.HistoryWithLimit(1)).Items(ctx)
	if err != nil {
		return fmt.Errorf("ably history: %w", err)
	}
	if !items.Next(ctx) {
		return errors.New("no messages on channel")
	}
	msg := items.Item()
	env, err := decodeMessage(msg, key)
	if err != nil {
		return err
	}
	if env.SenderID == cfg.SenderID {
		fmt.Printf("%s-%s Latest message is from this machine; nothing to apply.\n", ui.Dim, ui.NC)
		return nil
	}
	return applyEnvelope(env, name, dir)
}

// Subscribe is a long-running daemon that applies inbound messages.
func Subscribe(ctx context.Context, profile string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	key, err := LoadMasterKey()
	if err != nil {
		return err
	}
	name, dir, err := resolveProfileForSync(profile)
	if err != nil {
		return err
	}
	upn, err := AzureUPNFromProfile(dir)
	if err != nil {
		return err
	}
	channel := ChannelName(cfg.ChannelPrefix, upn, name)

	client, err := ably.NewRealtime(ably.WithKey(cfg.AblyAPIKey))
	if err != nil {
		return err
	}
	defer client.Close()
	ch := client.Channels.Get(channel, ably.ChannelWithParams("rewind", "1"))

	fmt.Printf("%s%s%s Subscribed as %s%s%s on %s%s%s\n",
		ui.Green, ui.Check, ui.NC,
		ui.Bold, cfg.SenderID, ui.NC,
		ui.Dim, channel, ui.NC)

	dedupe := newDedupe(50)

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	unsub, err := ch.Subscribe(sigCtx, ablyMessageName, func(msg *ably.Message) {
		env, err := decodeMessage(msg, key)
		if err != nil {
			fmt.Printf("  %s%s%s decode: %s\n", ui.Red, ui.Cross, ui.NC, err.Error())
			return
		}
		if env.SenderID == cfg.SenderID {
			return
		}
		if !dedupe.markNew(env.SenderID, env.MonotonicSeq) {
			return
		}
		if err := applyEnvelope(env, name, dir); err != nil {
			fmt.Printf("  %s%s%s apply: %s\n", ui.Red, ui.Cross, ui.NC, err.Error())
		}
	})
	if err != nil {
		return err
	}
	defer unsub()

	<-sigCtx.Done()
	fmt.Printf("\n%s-%s Subscriber exiting.\n", ui.Dim, ui.NC)
	return nil
}

func decodeMessage(msg *ably.Message, key []byte) (*Envelope, error) {
	s, ok := msg.Data.(string)
	if !ok {
		return nil, fmt.Errorf("unexpected message data type %T", msg.Data)
	}
	ct, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	pt, err := DecryptGCM(key, ct)
	if err != nil {
		return nil, err
	}
	return UnmarshalEnvelope(pt)
}

func applyEnvelope(env *Envelope, name, dir string) error {
	skewWarn := ""
	if fi, err := os.Stat(dir); err == nil {
		drift := time.Now().UnixMilli() - env.TsUnixMs
		if drift > 30_000 && fi.ModTime().UnixMilli() > env.TsUnixMs+30_000 {
			skewWarn = " (clock skew or out-of-order; applying anyway)"
		}
	}
	if err := env.ApplyTo(dir); err != nil {
		return err
	}
	RecordReceive(name)
	fmt.Printf("%s%s%s Applied %s%s%s from %s%s%s seq %d%s\n",
		ui.Green, ui.Check, ui.NC,
		ui.Bold, name, ui.NC,
		ui.Dim, env.SenderID, ui.NC,
		env.MonotonicSeq, skewWarn)
	return nil
}

// dedupe is a tiny ring of (sender_id, seq) pairs.
type dedupe struct {
	mu   sync.Mutex
	max  int
	seen map[string]int64 // sender_id -> highest seq seen
}

func newDedupe(max int) *dedupe {
	return &dedupe{max: max, seen: map[string]int64{}}
}

func (d *dedupe) markNew(sender string, seq int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur, ok := d.seen[sender]; ok && seq <= cur {
		return false
	}
	d.seen[sender] = seq
	if len(d.seen) > d.max {
		// drop a random entry — small N, doesn't matter which
		for k := range d.seen {
			delete(d.seen, k)
			break
		}
	}
	return true
}
