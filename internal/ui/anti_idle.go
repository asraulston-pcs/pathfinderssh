// internal/ui/anti_idle.go
// anti_idle.go - anti-idle keystroke timer for Session.
//
// Many devices enforce an application-layer idle timeout (Cisco `exec-timeout`,
// session managers, console servers) that an SSH/TCP keepalive does NOT defeat:
// the transport stays up, but the device logs you out because it saw no input.
// Anti-idle sends a real (configurable, non-disruptive) keystroke after a quiet
// interval, which the device counts as activity.
//
// This is complementary to the SSH keepalive (SSHConfig.KeepAliveInterval):
//   - keepalive keeps the *connection* alive (NAT/firewall/SSH layer)
//   - anti-idle keeps the *session* alive (the device's own idle timer)
//
// Lifecycle: started by readLoop on attach, stopped when readLoop exits, so it
// can never outlive a connection or leak across a reconnect. Activity tracking
// is free: writeOverride already funnels every user keystroke, and it bumps
// lastUserInput there. The anti-idle keystroke itself goes out via sendRaw,
// which bypasses writeOverride, so it neither resets the idle clock nor trips
// the reconnect prompt.

package ui

import (
	"context"
	"log"
	"strings"
	"time"
)

// AntiIdleConfig is the resolved, ready-to-run anti-idle setting for one
// session. Enabled==false means the loop is never started.
type AntiIdleConfig struct {
	Enabled  bool
	Interval time.Duration // quiet time before a keystroke is sent
	Payload  []byte        // the bytes to send (see antiIdlePayload)
	Label    string        // human label of the keystroke, for logging
}

const (
	antiIdleDefaultIntervalSec = 180 // 3 min: well under a 10-min exec-timeout
	antiIdleMinIntervalSec     = 10  // floor so a typo can't hammer the link
	antiIdleDefaultKeystroke   = "backspace"
)

// antiIdlePayload maps a friendly keystroke name to the bytes to send. The
// default, a lone backspace (DEL, 0x7f), is a no-op at an empty prompt.
// "space+backspace" is the fully non-destructive choice: it types a space then
// erases it, leaving the line buffer unchanged even mid-command.
func antiIdlePayload(name string) ([]byte, string) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "space+backspace", "space-backspace", "spacebs":
		return []byte{0x20, 0x7f}, "Space+Backspace"
	case "space":
		return []byte{0x20}, "Space"
	case "escape", "esc":
		return []byte{0x1b}, "Escape"
	case "", "backspace", "bs", "del", "delete":
		return []byte{0x7f}, "Backspace"
	default:
		return []byte{0x7f}, "Backspace"
	}
}

// AntiIdleKeystrokeChoices are the labels offered in the settings selector.
var AntiIdleKeystrokeChoices = []string{"backspace", "space+backspace", "escape", "space"}

// NewAntiIdleConfig builds a config from raw settings values, clamping the
// interval and resolving the keystroke. intervalSec <= 0 falls back to the
// default; anything below the floor is raised to it.
func NewAntiIdleConfig(enabled bool, intervalSec int, keystroke string) AntiIdleConfig {
	if intervalSec <= 0 {
		intervalSec = antiIdleDefaultIntervalSec
	}
	if intervalSec < antiIdleMinIntervalSec {
		intervalSec = antiIdleMinIntervalSec
	}
	payload, label := antiIdlePayload(keystroke)
	return AntiIdleConfig{
		Enabled:  enabled,
		Interval: time.Duration(intervalSec) * time.Second,
		Payload:  payload,
		Label:    label,
	}
}

// SetAntiIdle stores the resolved config to apply on the next attach. Call
// before ConnectX (the manager does this in doConnect*).
func (s *Session) SetAntiIdle(cfg AntiIdleConfig) {
	s.antiIdle = cfg
}

// AntiIdleOverride is a per-session override of the global anti-idle settings.
// Both fields are optional; a nil field means "use the global value". It
// replaces the SessionInfo parameter this took in TetherSSH -- the session
// model belongs to the layer above, and this package only needs the two values
// it can override.
type AntiIdleOverride struct {
	Enabled     *bool
	IntervalSec *int
}

// ResolveAntiIdle builds the effective anti-idle config: the global settings,
// with any per-session override applied. Pass nil for an ad-hoc session that
// has no stored definition.
func ResolveAntiIdle(over *AntiIdleOverride) AntiIdleConfig {
	cfg := CurrentSettings()
	enabled := cfg.AntiIdleEnabled
	intervalSec := cfg.AntiIdleIntervalSec
	if over != nil {
		if over.Enabled != nil {
			enabled = *over.Enabled
		}
		if over.IntervalSec != nil && *over.IntervalSec > 0 {
			intervalSec = *over.IntervalSec
		}
	}
	return NewAntiIdleConfig(enabled, intervalSec, cfg.AntiIdleKeystroke)
}

// noteUserInput records real user activity; called from writeOverride. Cheap
// enough to run on every keystroke.
func (s *Session) noteUserInput() {
	s.lastUserInput.Store(time.Now().UnixNano())
}

// startAntiIdle launches the idle-timer goroutine bound to ctx (the readLoop's
// context). It is a no-op when anti-idle is disabled. Any prior loop is stopped
// first, so re-attach can't double-run it.
func (s *Session) startAntiIdle(ctx context.Context) {
	s.stopAntiIdle()
	if !s.antiIdle.Enabled || s.antiIdle.Interval <= 0 {
		return
	}
	aiCtx, cancel := context.WithCancel(ctx)
	s.cancelAntiIdle = cancel
	cfg := s.antiIdle
	log.Printf("anti-idle: enabled, every %s, key=%s", cfg.Interval, cfg.Label)
	go s.antiIdleLoop(aiCtx, cfg)
}

// stopAntiIdle cancels the idle-timer goroutine if running.
func (s *Session) stopAntiIdle() {
	if s.cancelAntiIdle != nil {
		s.cancelAntiIdle()
		s.cancelAntiIdle = nil
	}
}

// antiIdleLoop sends the keystroke whenever the session has been quiet for at
// least cfg.Interval. It polls at a fraction of the interval (bounded to a sane
// range) so the keystroke lands within ~Interval+resolution of going idle,
// rather than up to a full interval late. After sending, it re-seeds the idle
// clock so the next keystroke is another full interval away - i.e. while truly
// idle it sends roughly once per interval; while the user is typing it sends
// nothing.
func (s *Session) antiIdleLoop(ctx context.Context, cfg AntiIdleConfig) {
	res := cfg.Interval / 4
	if res < time.Second {
		res = time.Second
	}
	if res > 15*time.Second {
		res = 15 * time.Second
	}
	t := time.NewTicker(res)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !s.Connected() {
				continue
			}
			last := time.Unix(0, s.lastUserInput.Load())
			if time.Since(last) >= cfg.Interval {
				s.SendRaw(cfg.Payload)
				// Re-arm: count from this send, not from the last real key.
				s.lastUserInput.Store(time.Now().UnixNano())
			}
		}
	}
}
