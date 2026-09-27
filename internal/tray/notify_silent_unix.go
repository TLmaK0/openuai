//go:build (linux || freebsd || netbsd || openbsd || illumos) && !nodbus

package tray

import (
	"io"
	"log"
	"os/exec"
	"time"

	"github.com/esiqveland/notify"
	"github.com/gen2brain/beeep"
	"github.com/godbus/dbus/v5"
)

// notifySilent shows a notification asking the notification server not to
// play any sound (freedesktop "suppress-sound" hint). If neither D-Bus nor
// notify-send can deliver it, the notification is dropped instead of going
// through a path that could make a sound.
func notifySilent(title, message, icon string) error {
	if err := notifySilentDBus(title, message, icon); err == nil {
		return nil
	}
	cmd, err := exec.LookPath("notify-send")
	if err != nil {
		return err
	}
	args := []string{title, message, "-a", beeep.AppName, "-u", "normal", "-h", "boolean:suppress-sound:true"}
	if icon != "" {
		args = append(args, "-i", icon)
	}
	return exec.Command(cmd, args...).Run()
}

func notifySilentDBus(title, message, icon string) error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return err
	}
	n := notify.Notification{
		AppName:       beeep.AppName,
		AppIcon:       icon,
		Summary:       title,
		Body:          message,
		ExpireTimeout: 5 * time.Second,
		Hints:         map[string]dbus.Variant{"suppress-sound": dbus.MakeVariant(true)},
	}
	n.SetUrgency(notify.UrgencyNormal)

	notifier, err := notify.New(conn, notify.WithLogger(log.New(io.Discard, "", log.Flags())))
	if err != nil {
		return err
	}
	defer notifier.Close()
	_, err = notifier.SendNotification(n)
	return err
}
